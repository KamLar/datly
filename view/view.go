package view

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/viant/afs"
	"github.com/viant/afs/url"
	"github.com/viant/datly/logger"
	"github.com/viant/datly/shared"
	"github.com/viant/gmetric/provider"
	"github.com/viant/sqlx/io"
	"github.com/viant/sqlx/option"
	"github.com/viant/toolbox/format"
	"reflect"
	"strings"
	"time"
)

type (
	//View represents a view View
	View struct {
		shared.Reference
		Connector  *Connector
		Standalone bool `json:",omitempty"`
		Name       string
		Alias      string `json:",omitempty"`
		Table      string `json:",omitempty"`
		From       string `json:",omitempty"`
		FromURL    string

		Exclude              []string   `json:",omitempty"`
		Columns              []*Column  `json:",omitempty"`
		InheritSchemaColumns bool       `json:",omitempty"`
		CaseFormat           CaseFormat `json:",omitempty"`

		Criteria string `json:",omitempty"`

		Selector            *Config      `json:",omitempty"`
		SelectorConstraints *Constraints `json:",omitempty"`
		Template            *Template    `json:",omitempty"`

		Schema *Schema `json:",omitempty"`

		With []*Relation `json:",omitempty"`

		MatchStrategy MatchStrategy `json:",omitempty"`
		Batch         *Batch        `json:",omitempty"`

		Logger  *logger.Adapter `json:"-"`
		Counter logger.Counter  `json:"-"`

		_columns  Columns
		_excluded map[string]bool

		Caser        format.Case `json:",omitempty"`
		initialized  bool
		newCollector func(dest interface{}, supportParallel bool) *Collector
	}

	//Constraints configure what can be selected by Selector
	//For each field, default value is `false`
	Constraints struct {
		Criteria          bool
		OrderBy           bool
		Limit             bool
		Offset            bool
		FilterableColumns []string
	}

	Batch struct {
		Parent int `json:",omitempty"`
	}
)

//DataType returns struct type.
func (v *View) DataType() reflect.Type {
	return v.Schema.Type()
}

//Init initializes View using view provided in Resource.
//i.e. If View, Connector etc. should inherit from others - it has te bo included in Resource.
//It is important to call Init for every View because it also initializes due to the optimization reasons.
func (v *View) Init(ctx context.Context, resource *Resource) error {
	if v.initialized {
		return nil
	}
	err := v.loadFromWithURL(ctx, resource)
	if err != nil {
		return err
	}
	if err := v.initViews(ctx, resource, v.With); err != nil {
		return err
	}

	if err := v.initView(ctx, resource); err != nil {
		return err
	}

	if err := v.updateRelations(ctx, resource, v.With); err != nil {
		return err
	}

	v.initialized = true
	return nil
}

func (v *View) loadFromWithURL(ctx context.Context, resource *Resource) error {
	if v.FromURL == "" {
		return nil
	}
	if url.Scheme(v.FromURL, "empty") == "empty" && resource.SourceURL != "" {
		v.FromURL = url.Join(resource.SourceURL, v.FromURL)
	}
	fs := afs.New()
	data, err := fs.DownloadWithURL(ctx, v.FromURL)
	if err != nil {
		return err
	}
	v.FromURL = string(data)
	return nil
}

func (v *View) initViews(ctx context.Context, resource *Resource, relations []*Relation) error {
	for _, rel := range relations {
		refView := &rel.Of.View
		v.generateNameIfNeeded(refView, rel)
		if err := refView.inheritFromViewIfNeeded(ctx, resource); err != nil {
			return err
		}

		if err := rel.BeforeViewInit(ctx); err != nil {
			return err
		}

		if err := refView.initViews(ctx, resource, refView.With); err != nil {
			return err
		}

		if err := refView.initView(ctx, resource); err != nil {
			return err
		}

	}
	return nil
}

func (v *View) generateNameIfNeeded(refView *View, rel *Relation) {
	if refView.Name == "" {
		refView.Name = v.Name + "#rel:" + rel.Name
	}
}

func (v *View) initView(ctx context.Context, resource *Resource) error {
	var err error
	if err = v.inheritFromViewIfNeeded(ctx, resource); err != nil {
		return err
	}

	v.ensureBatch()

	if err = v.ensureLogger(resource); err != nil {
		return err
	}

	v.ensureCounter(resource)

	v.Alias = notEmptyOf(v.Alias, "t")
	if v.From == "" {
		v.Table = notEmptyOf(v.Table, v.Name)
	}

	if v.MatchStrategy == "" {
		v.MatchStrategy = ReadMatched
	}
	if err = v.MatchStrategy.Validate(); err != nil {
		return err
	}

	if v.Selector == nil {
		v.Selector = &Config{}
	}

	v.ensureSelectorConstraints()

	if v.Name == v.Ref && !v.Standalone {
		return fmt.Errorf("view name and ref cannot be the same")
	}

	if v.Name == "" {
		return fmt.Errorf("view name was empty")
	}

	if v.Connector, err = resource.FindConnector(v); err != nil {
		return err
	}

	if err = v.Connector.Init(ctx, resource._connectors); err != nil {
		return err
	}

	if err = v.Connector.Validate(); err != nil {
		return err
	}

	if err = v.ensureCaseFormat(); err != nil {
		return err
	}

	if err = v.initTemplate(ctx, resource); err != nil {
		return err
	}

	if err = v.ensureColumns(ctx); err != nil {
		return err
	}

	if err = ColumnSlice(v.Columns).Init(v.Caser); err != nil {
		return err
	}

	v._columns = ColumnSlice(v.Columns).Index(v.Caser)
	if err = v.markColumnsAsFilterable(); err != nil {
		return err
	}

	v.ensureIndexExcluded()

	if err = v.ensureSchema(resource._types); err != nil {
		return err
	}

	v.updateColumnTypes()
	return nil
}

func (v *View) ensureCounter(resource *Resource) {
	if v.Counter != nil {
		return
	}
	var counter logger.Counter
	if metric := resource.Metrics; metric != nil {
		name := v.Name
		if metric.URIPart != "" {
			name = metric.URIPart + name
		}
		name = strings.ReplaceAll(name, "/", ".")
		if metric.Service.LookupCounter(name) == nil {
			counter = metric.Service.MultiOperationCounter(metricLocation(), name, name+" performance", time.Millisecond, time.Minute, 2, provider.NewBasic())
		}
	}
	v.Counter = logger.NewCounter(counter)

}

func (v *View) updateColumnTypes() {
	rType := shared.Elem(v.DataType())
	for i := 0; i < rType.NumField(); i++ {
		field := rType.Field(i)

		column, err := v._columns.Lookup(field.Name)
		if err != nil {
			continue
		}

		column.setField(field)
	}
}

func (v *View) updateRelations(ctx context.Context, resource *Resource, relations []*Relation) error {
	v.indexColumns()
	if err := v.indexSqlxColumnsByFieldName(); err != nil {
		return err
	}

	v.ensureCollector()

	if err := v.deriveColumnsFromSchema(nil); err != nil {
		return err
	}

	for _, rel := range relations {
		if err := rel.Init(ctx, resource, v); err != nil {
			return err
		}

		refView := rel.Of.View
		if err := refView.updateRelations(ctx, resource, refView.With); err != nil {
			return err
		}
	}

	if err := v.registerHolders(); err != nil {
		return err
	}

	return nil
}

func (v *View) ensureColumns(ctx context.Context) error {
	if len(v.Columns) != 0 {
		return nil
	}

	SQL := detectColumnsSQL(v.Source(), v)
	v.Logger.ColumnsDetection(SQL, v.Source())
	columns, err := detectColumns(ctx, SQL, v)

	if err != nil {
		return err
	}

	if v.From != "" && v.Table != "" {
		tableSQL := detectColumnsSQL(v.Table, v)
		v.Logger.ColumnsDetection(tableSQL, v.Table)
		tableColumns, err := detectColumns(ctx, tableSQL, v)
		if err != nil {
			return err
		}

		ColumnSlice(columns).updateTypes(tableColumns, v.Caser)
		v.Columns = columns
	} else {
		v.Columns = columns
	}

	return nil
}

func convertIoColumnsToColumns(ioColumns []io.Column, nullable map[string]bool) []*Column {
	columns := make([]*Column, 0)
	for i := 0; i < len(ioColumns); i++ {
		scanType := ioColumns[i].ScanType()
		dataTypeName := ioColumns[i].DatabaseTypeName()
		columns = append(columns, &Column{
			Name:     ioColumns[i].Name(),
			DataType: dataTypeName,
			rType:    scanType,
			Nullable: nullable[ioColumns[i].Name()],
		})
	}
	return columns
}

//ColumnByName returns Column by Column.Name
func (v *View) ColumnByName(name string) (*Column, bool) {
	if column, ok := v._columns[name]; ok {
		return column, true
	}

	return nil, false
}

//Source returns database view source. It prioritizes From, Table then View.Name
func (v *View) Source() string {
	if v.From != "" {
		if v.From[0] == '(' {
			return v.From
		}
		return "(" + v.From + ")"
	}

	if v.Table != "" {
		return v.Table
	}

	return v.Name
}

func (v *View) ensureSchema(types Types) error {
	v.initSchemaIfNeeded()
	if v.Schema.Name != "" {
		componentType, err := types.Lookup(v.Schema.Name)
		if err != nil {
			return err
		}

		if componentType != nil {
			v.Schema.setType(componentType)
		}
	}

	return v.Schema.Init(v.Columns, v.With, v.Caser, types)
}

//Db returns database connection that View was assigned to.
func (v *View) Db() (*sql.DB, error) {
	return v.Connector.Db()
}

func (v *View) exclude(columns []io.Column) []io.Column {
	if len(v.Exclude) == 0 {
		return columns
	}

	filtered := make([]io.Column, 0, len(columns))
	for i := range columns {
		if _, ok := v._excluded[columns[i].Name()]; ok {
			continue
		}
		filtered = append(filtered, columns[i])
	}
	return filtered
}

func (v *View) inherit(view *View) {
	if v.Connector == nil {
		v.Connector = view.Connector
	}

	v.Alias = notEmptyOf(v.Alias, view.Alias)
	v.Table = notEmptyOf(v.Table, view.Table)
	v.From = notEmptyOf(v.From, view.From)

	if len(v.Columns) == 0 {
		v.Columns = view.Columns
	}

	v.Criteria = notEmptyOf(v.Criteria, view.Criteria)

	if v.Schema == nil && len(v.With) == 0 {
		v.Schema = view.Schema
	}

	if len(v.With) == 0 {
		v.With = view.With
	}

	if len(v.Exclude) == 0 {
		v.Exclude = view.Exclude
	}

	if v.CaseFormat == "" {
		v.CaseFormat = view.CaseFormat
		v.Caser = view.Caser
	}

	if v.newCollector == nil && len(v.With) == 0 {
		v.newCollector = view.newCollector
	}

	if v.Template == nil {
		v.Template = view.Template
	}

	if v.MatchStrategy == "" {
		v.MatchStrategy = view.MatchStrategy
	}

	if v.Selector == nil {
		v.Selector = view.Selector
	}

	if v.SelectorConstraints == nil {
		v.SelectorConstraints = view.SelectorConstraints
	}

	if v.Logger == nil {
		v.Logger = view.Logger
	}

	if v.Batch == nil {
		v.Batch = view.Batch
	}
}

func (v *View) ensureIndexExcluded() {
	if len(v.Exclude) == 0 {
		return
	}

	v._excluded = Names(v.Exclude).Index()
}

//SelectedColumns returns columns selected by Selector if it is allowed by the View to use Selector.Columns
//(see Constraints.Columns) or View.Columns
func (v *View) SelectedColumns(selector *Selector) ([]*Column, error) {
	if !v.CanUseSelectorColumns() || selector == nil || len(selector.Columns) == 0 {
		return v.Columns, nil
	}

	result := make([]*Column, len(selector.Columns))
	for i, name := range selector.Columns {
		column, ok := v._columns[name]
		if !ok {
			return nil, fmt.Errorf("invalid column name: %v", name)
		}
		result[i] = column
	}
	return result, nil
}

func (v *View) ensureCaseFormat() error {
	if err := v.CaseFormat.Init(); err != nil {
		return err
	}

	var err error
	v.Caser, err = v.CaseFormat.Caser()
	return err
}

func (v *View) ensureCollector() {
	v.newCollector = func(dest interface{}, supportParallel bool) *Collector {
		return NewCollector(v.Schema.slice, v, dest, supportParallel)
	}
}

//Collector creates new Collector for View.DataType
func (v *View) Collector(dest interface{}, supportParallel bool) *Collector {
	return v.newCollector(dest, supportParallel)
}

func notEmptyOf(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}

	return ""
}

func (v *View) registerHolders() error {
	for i := range v.With {
		if err := v._columns.RegisterHolder(v.With[i]); err != nil {
			return err
		}
	}

	return nil
}

//LimitWithSelector returns Selector.Limit if it is allowed by the View to use Selector.Columns (see Constraints.Limit)
func (v *View) LimitWithSelector(selector *Selector) int {
	if v.CanUseSelectorLimit() && selector != nil && selector.Limit > 0 {
		return selector.Limit
	}
	return v.Selector.Limit
}

func (v *View) ensureSelectorConstraints() {
	if v.SelectorConstraints == nil {
		v.SelectorConstraints = &Constraints{}
	}

}

//CanUseSelectorCriteria indicates if Selector.Criteria can be used
func (v *View) CanUseSelectorCriteria() bool {
	return v.SelectorConstraints.Criteria
}

//CanUseSelectorColumns indicates if Selector.Columns can be used
func (v *View) CanUseSelectorColumns() bool {
	return len(v.SelectorConstraints.FilterableColumns) != 0
}

//CanUseSelectorLimit indicates if Selector.Limit can be used
func (v *View) CanUseSelectorLimit() bool {
	return v.SelectorConstraints.Limit
}

//CanUseSelectorOrderBy indicates if Selector.OrderBy can be used
func (v *View) CanUseSelectorOrderBy() bool {
	return v.SelectorConstraints.OrderBy
}

//CanUseSelectorOffset indicates if Selector.Offset can be used
func (v *View) CanUseSelectorOffset() bool {
	return v.SelectorConstraints.Offset
}

//IndexedColumns returns Columns
func (v *View) IndexedColumns() Columns {
	return v._columns
}

func (v *View) markColumnsAsFilterable() error {
	if len(v.SelectorConstraints.FilterableColumns) == 1 && strings.TrimSpace(v.SelectorConstraints.FilterableColumns[0]) == "*" {
		for _, column := range v.Columns {
			column.Filterable = true
		}

		return nil
	}

	for _, colName := range v.SelectorConstraints.FilterableColumns {
		column, err := v._columns.Lookup(colName)
		if err != nil {
			return fmt.Errorf("invalid view: %v %w", v.Name, err)
		}
		column.Filterable = true
	}
	return nil
}

func (v *View) indexSqlxColumnsByFieldName() error {
	rType := shared.Elem(v.Schema.Type())
	for i := 0; i < rType.NumField(); i++ {
		field := rType.Field(i)
		isExported := field.PkgPath == ""
		if !isExported {
			continue
		}

		tag := io.ParseTag(field.Tag.Get(option.TagSqlx))
		//TODO: support anonymous fields
		if tag.Column != "" {
			column, err := v._columns.Lookup(tag.Column)
			if err != nil {
				return fmt.Errorf("invalid view: %v %w", v.Name, err)
			}
			v._columns.RegisterWithName(field.Name, column)
		}
	}

	return nil
}

func (v *View) deriveColumnsFromSchema(relation *Relation) error {
	if !v.InheritSchemaColumns {
		return nil
	}

	newColumns := make([]*Column, 0)

	if err := v.updateColumn(shared.Elem(v.Schema.Type()), &newColumns, relation); err != nil {
		return err
	}

	v.Columns = newColumns
	v._columns = ColumnSlice(newColumns).Index(v.Caser)

	return nil
}

func (v *View) updateColumn(rType reflect.Type, columns *[]*Column, relation *Relation) error {
	index := ColumnSlice(*columns).Index(v.Caser)

	for i := 0; i < rType.NumField(); i++ {
		field := rType.Field(i)
		isExported := field.PkgPath == ""
		if !isExported {
			continue
		}
		if field.Anonymous {
			if err := v.updateColumn(field.Type, columns, relation); err != nil {
				return err
			}
			continue
		}

		if _, ok := index[field.Name]; ok {
			continue
		}

		column, err := v._columns.Lookup(field.Name)
		if err == nil {
			*columns = append(*columns, column)
		}

		tag := io.ParseTag(field.Tag.Get(option.TagSqlx))
		column, err = v._columns.Lookup(tag.Column)
		if err == nil {
			*columns = append(*columns, column)
			index.Register(v.Caser, column)
		}
	}

	for _, rel := range v.With {
		if _, ok := index[rel.Of.Column]; ok {
			continue
		}

		col, err := v._columns.Lookup(rel.Column)
		if err != nil {
			return fmt.Errorf("invalid rel: %v %w", rel.Name, err)
		}

		*columns = append(*columns, col)
	}

	if relation != nil {
		_, err := index.Lookup(relation.Of.Column)
		if err != nil {
			col, err := v._columns.Lookup(relation.Of.Column)
			if err != nil {
				return fmt.Errorf("invalid ref: %v %w", relation.Name, err)
			}
			*columns = append(*columns, col)
		}
	}

	return nil
}

func (v *View) initSchemaIfNeeded() {
	if v.Schema == nil {
		v.Schema = &Schema{
			autoGen: true,
		}
	}
}

func (v *View) inheritFromViewIfNeeded(ctx context.Context, resource *Resource) error {
	if v.Ref != "" {
		view, err := resource._views.Lookup(v.Ref)
		if err != nil {
			return err
		}

		if err = view.initView(ctx, resource); err != nil {
			return err
		}
		v.inherit(view)
	}
	return nil
}

func (v *View) indexColumns() {
	v._columns = ColumnSlice(v.Columns).Index(v.Caser)
}

func (v *View) ensureLogger(resource *Resource) error {
	if v.Logger == nil {
		v.Logger = logger.Default()
		return nil
	}

	if v.Logger.Ref != "" {
		adapter, ok := resource._loggers.Lookup(v.Logger.Ref)
		if !ok {
			return fmt.Errorf("not found Logger %v in Resource", v.Logger.Ref)
		}

		v.Logger.Inherit(adapter)
	}

	return nil
}

func (v *View) ensureBatch() {
	if v.Batch != nil {
		return
	}

	v.Batch = &Batch{
		Parent: 10000,
	}
}

func (v *View) initTemplate(ctx context.Context, res *Resource) error {
	if v.Template == nil {
		v.Template = &Template{}
	}

	return v.Template.Init(ctx, res, v)
}