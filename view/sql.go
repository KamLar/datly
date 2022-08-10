package view

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/viant/datly/reader/metadata"
	"github.com/viant/datly/view/keywords"
	"github.com/viant/sqlx/io"
	"github.com/viant/sqlx/io/config"
	rdata "github.com/viant/toolbox/data"
	"reflect"
	"strings"
)

type (
	TemplateEvaluation struct {
		SQL       string
		Evaluated bool
		Expander  ExpanderFn
	}

	ExpanderFn func(placeholders *[]interface{}, SQL string, selector *Selector, params CommonParams, batchData *BatchData) (string, error)
)

func DetectColumns(ctx context.Context, resource *Resource, v *View) ([]*Column, string, error) {
	evaluation, err := evaluateTemplateIfNeeded(ctx, resource, v)
	if err != nil {
		return nil, "", err
	}

	columns, SQL, err := detectColumns(ctx, evaluation, v)
	if err != nil {
		return expandWithoutTemplateEvaluation(ctx, evaluation, SQL, err, columns, v)
	}

	if v.From != "" && v.Table != "" {
		tableColumns, tableSQL, errr := detectColumns(ctx, &TemplateEvaluation{SQL: v.Table}, v)
		if errr != nil {
			return nil, tableSQL, errr
		}

		v.Logger.ColumnsDetection(tableSQL, v.Table)
		if err != nil {
			return nil, tableSQL, err
		}

		Columns(columns).updateTypes(tableColumns, v.Caser)
	}

	return columns, SQL, nil
}

func expandWithoutTemplateEvaluation(ctx context.Context, evaluation *TemplateEvaluation, SQL string, err error, columns []*Column, v *View) ([]*Column, string, error) {
	if evaluation.Evaluated {
		return columns, SQL, err
	}

	fmt.Println(fmt.Errorf("failed to detect columns using velocity engine and SQL:  %v  due to the %w\n", SQL, err).Error())

	columns, SQL, err = detectColumns(ctx, &TemplateEvaluation{SQL: v.Source(), Expander: v.Expand}, v)
	if err != nil {
		return nil, "", fmt.Errorf("failed also to detect columns using %v due to the %w\n", SQL, err)
	}

	return columns, SQL, nil
}

func evaluateTemplateIfNeeded(ctx context.Context, resource *Resource, aView *View) (evaluation *TemplateEvaluation, err error) {
	result := &TemplateEvaluation{
		Expander: aView.Expand,
	}

	if aView.ForceSource == TableSourceType {
		result.SQL = aView.Table
		result.Expander = nil
		return result, nil
	}

	if aView.Template == nil {
		result.SQL = aView.Source()
		return result, nil
	}

	if err := aView.Template.Init(ctx, resource, aView); err != nil {
		return nil, err
	}

	params := newValue(aView.Template.Schema.Type())
	presence := newValue(aView.Template.PresenceSchema.Type())

	source, _, err := aView.Template.EvaluateSource(params, presence, aView)
	if err != nil {
		return nil, err
	}

	source, err = expandWithZeroValues(source, aView.Template)
	if err != nil {
		return nil, err
	}

	result.SQL = source
	return result, nil
}

func detectColumns(ctx context.Context, evaluation *TemplateEvaluation, v *View) ([]*Column, string, error) {
	SQL, args, err := detectColumnsSQL(evaluation, v)
	if err != nil {
		return nil, "", err
	}

	aDb, err := v.Connector.DB()

	if err != nil {
		return nil, SQL, err
	}

	query, err := aDb.QueryContext(ctx, SQL, args...)
	if err != nil {
		return nil, SQL, err
	}

	types, err := query.ColumnTypes()
	if err != nil {
		return nil, SQL, err
	}

	ioColumns := io.TypesToColumns(types)
	columnsMetadata, err := columnsMetadata(ctx, aDb, v, ioColumns)
	if err != nil {
		return nil, SQL, err
	}

	columns := convertIoColumnsToColumns(v.exclude(ioColumns), columnsMetadata)
	return columns, SQL, nil
}

func columnsMetadata(ctx context.Context, db *sql.DB, v *View, columns []io.Column) (map[string]bool, error) {
	if v.Source() != v.Table && v.Table != "" {
		return nil, nil
	}

	if len(columns) > 0 {
		if _, ok := columns[0].Nullable(); ok {
			result := map[string]bool{}
			for _, column := range columns {
				result[column.Name()], _ = column.Nullable()
			}
			return result, nil
		}
	}

	session, err := config.Session(ctx, db)
	if err != nil {
		return nil, err
	}

	sinkColumns, err := config.Columns(ctx, session, db, v.Table)
	if err != nil {
		return nil, err
	}

	result := map[string]bool{}
	for _, column := range sinkColumns {
		result[column.Name] = strings.EqualFold(column.Nullable, "YES") || strings.EqualFold(column.Nullable, "1") || strings.EqualFold(column.Nullable, "TRUE")
	}

	return result, nil
}

func detectColumnsSQL(evaluation *TemplateEvaluation, v *View) (string, []interface{}, error) {
	source := evaluation.SQL

	sb := strings.Builder{}
	sb.WriteString("SELECT ")
	if v.Alias != "" {
		sb.WriteString(v.Alias)
		sb.WriteString(".")
	}
	sb.WriteString("* FROM ")
	sb.WriteString(source)
	sb.WriteString(" ")
	sb.WriteString(v.Alias)
	sb.WriteString(" WHERE 1=0")

	SQL := sb.String()
	if source != v.Name && source != v.Table {
		SQL = ExpandWithFalseCondition(source)
	}

	var placeholders []interface{}
	var err error

	if evaluation.Expander != nil {
		SQL, err = v.Expand(&placeholders, SQL, &Selector{}, CommonParams{}, &BatchData{})
		if err != nil {
			return SQL, nil, err
		}
	}

	return SQL, placeholders, nil
}

func ExpandWithFalseCondition(source string) string {
	discover := metadata.EnrichWithDiscover(source, false)
	replacement := rdata.Map{}
	replacement.Put(keywords.AndCriteria[1:], "\n\n AND 1=0 ")
	replacement.Put(keywords.WhereCriteria[1:], "\n\n WHERE 1=0 ")
	SQL := replacement.ExpandAsText(discover)
	return SQL
}

func expandWithZeroValues(SQL string, template *Template) (string, error) {
	expandMap := rdata.Map{}
	for _, parameter := range template.Parameters {
		var value interface{}
		paramType := parameter.Schema.Type()
		switch paramType.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			value = 0
		case reflect.Float32, reflect.Float64:
			value = 0.0
		case reflect.String:
			value = "''"
		case reflect.Bool:
			value = false
		default:
			value = reflect.New(paramType).Elem().Interface()
		}

		expandMap.SetValue(parameter.Name, value)
	}

	return expandMap.ExpandAsText(SQL), nil
}
