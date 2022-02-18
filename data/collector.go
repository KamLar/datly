package data

import (
	"fmt"
	"github.com/viant/sqlx/io"
	"github.com/viant/xunsafe"
	"reflect"
	"unsafe"
)

//Visitor represents visitor function
type Visitor func(value interface{}) error

//Collector represents unmatched column resolver
type Collector struct {
	parent *Collector

	dest           interface{}
	appender       *xunsafe.Appender
	allowedColumns map[string]bool
	valuePosition  map[string]map[interface{}][]int //stores positions in main slice, based on field name, indexed by field value.
	types          map[string]*xunsafe.Type
	relation       *Relation

	values map[string]*[]interface{} //acts as a buffer. Value resolved with Resolve method can't be put to the value position map
	// because value fetched from database was not scanned into yet. Putting value to the map as a key, would create key as a pointer to the zero value.

	err       error
	slice     *xunsafe.Slice
	view      *View
	relations []*Collector

	supportParallel bool
}

//Resolve resolved unmapped column
func (r *Collector) Resolve(column io.Column) func(ptr unsafe.Pointer) interface{} {
	buffer, ok := r.values[column.Name()]
	if !ok {
		localSlice := make([]interface{}, 0)
		buffer = &localSlice
		r.values[column.Name()] = buffer
	}

	scanType := column.ScanType()
	kind := column.ScanType().Kind()
	switch kind {
	case reflect.Int, reflect.Int64, reflect.Uint, reflect.Uint64:
		scanType = reflect.TypeOf(0)
	}
	r.types[column.Name()] = xunsafe.NewType(scanType)
	return func(ptr unsafe.Pointer) interface{} {
		if !r.columnAllowed(column) {
			r.err = fmt.Errorf("can't resolve column %v", column.Name())
		}

		var valuePtr interface{}
		switch kind {
		case reflect.Int, reflect.Int64, reflect.Uint, reflect.Uint64:
			value := 0
			valuePtr = &value
		case reflect.Float64:
			value := 0.0
			valuePtr = &value
		case reflect.Bool:
			value := false
			valuePtr = &value
		case reflect.String:
			value := ""
			valuePtr = &value
		default:
			valuePtr = reflect.New(scanType).Interface()
		}
		*buffer = append(*buffer, valuePtr)

		return valuePtr
	}
}

func (r *Collector) columnAllowed(column io.Column) bool {
	if len(r.allowedColumns) == 0 {
		return true
	}
	if _, ok := r.allowedColumns[column.Name()]; ok {
		return true
	}
	return false
}

//parentValuesPositions returns positions in the main slice by given column name.
//After first use, it is not possible to index new resolved column indexes by Resolve method.
func (r *Collector) parentValuesPositions(columnName string) map[interface{}][]int {
	result, ok := r.parent.valuePosition[columnName]
	if !ok {
		r.indexParentPositions(columnName)
		result = r.parent.valuePosition[columnName]
	}
	return result
}

//NewCollector creates a collector
func NewCollector(columns []string, slice *xunsafe.Slice, view *View, dest interface{}, supportParallel bool) *Collector {
	var allowedColumns map[string]bool
	if len(columns) != 0 {
		allowedColumns = make(map[string]bool)
		for i := range columns {
			allowedColumns[columns[i]] = true
		}
	}

	ensuredDest := ensureDest(dest, view)

	return &Collector{
		dest:            ensuredDest,
		allowedColumns:  allowedColumns,
		valuePosition:   make(map[string]map[interface{}][]int),
		appender:        slice.Appender(xunsafe.AsPointer(ensuredDest)),
		slice:           slice,
		view:            view,
		types:           make(map[string]*xunsafe.Type),
		values:          make(map[string]*[]interface{}),
		supportParallel: supportParallel,
	}
}

func ensureDest(dest interface{}, view *View) interface{} {
	if _, ok := dest.(*interface{}); ok {
		return reflect.MakeSlice(view.Schema.SliceType(), 0, 1).Interface()
	}
	return dest
}

//Visitor creates visitor function
func (r *Collector) Visitor() func(value interface{}) error {
	relation := r.relation
	visitorRelations := RelationsSlice(r.view.With).PopulateWithVisitor()
	//if len(visitorRelations) == 0 && relation == nil {
	//	return func(value interface{}) error {
	//		return nil
	//	}
	//}

	for _, rel := range visitorRelations {
		r.valuePosition[rel.Column] = map[interface{}][]int{}
	}

	if relation == nil {
		return r.parentVisitor(visitorRelations)
	}

	switch relation.Cardinality {
	case "One":
		return r.visitorOne(relation)
	case "Many":
		return r.visitorMany(relation)
	}

	return func(owner interface{}) error {
		return nil
	}
}

func (r *Collector) parentVisitor(visitorRelations []*Relation) func(value interface{}) error {
	counter := 0
	return func(value interface{}) error {
		ptr := xunsafe.AsPointer(value)
		for _, rel := range visitorRelations {
			fieldValue := rel.columnField.Value(ptr)
			_, ok := r.valuePosition[rel.Column][fieldValue]
			if !ok {
				r.valuePosition[rel.Column][fieldValue] = []int{counter}
			} else {
				r.valuePosition[rel.Column][fieldValue] = append(r.valuePosition[rel.Column][fieldValue], counter)
			}
			counter++
		}
		return nil
	}
}

func (r *Collector) visitorOne(relation *Relation, visitors ...Visitor) func(value interface{}) error {
	keyField := relation.Of.field
	holderField := relation.holderField
	return func(owner interface{}) error {
		if r.parent != nil && r.parent.SupportsParallel() {
			return nil
		}

		for i := range visitors {
			if err := visitors[i](owner); err != nil {
				return err
			}
		}

		dest := r.parent.Dest()
		destPtr := xunsafe.AsPointer(dest)
		key := keyField.Interface(xunsafe.AsPointer(owner))
		valuePosition := r.parentValuesPositions(relation.Column)
		positions, ok := valuePosition[key]
		if !ok {
			return nil
		}

		for _, index := range positions {
			item := r.parent.slice.ValuePointerAt(destPtr, index)
			holderField.SetValue(xunsafe.AsPointer(item), owner)
		}
		return nil
	}
}

func (r *Collector) visitorMany(relation *Relation, visitors ...Visitor) func(value interface{}) error {
	keyField := relation.Of.field
	holderField := relation.holderField
	return func(owner interface{}) error {
		if r.parent != nil && r.parent.SupportsParallel() {
			return nil
		}

		for i := range visitors {
			if err := visitors[i](owner); err != nil {
				return err
			}
		}

		dest := r.parent.Dest()
		destPtr := xunsafe.AsPointer(dest)
		if dest == nil {
			return fmt.Errorf("dest was nil")
		}

		key := keyField.Interface(xunsafe.AsPointer(owner))
		valuePosition := r.parentValuesPositions(relation.Column)
		positions, ok := valuePosition[key]
		if !ok {
			return nil
		}

		for _, index := range positions {
			parentItem := r.parent.slice.ValuePointerAt(destPtr, index)
			sliceAddPtr := holderField.Pointer(xunsafe.AsPointer(parentItem))
			slice := relation.Of.Schema.Slice()
			appender := slice.Appender(sliceAddPtr)
			appender.Append(owner)
		}

		return nil
	}
}

//NewItem creates and return item provider.
//If view has no relations or is the main view, created item will be automatically appended to a slice.
func (r *Collector) NewItem() func() interface{} {
	return func() interface{} {
		add := r.appender.Add()
		return add
	}
}

func (r *Collector) indexParentPositions(name string) {
	if r.parent == nil {
		return
	}

	values := r.parent.values[name]
	xType := r.parent.types[name]
	r.parent.valuePosition[name] = map[interface{}][]int{}
	for position, v := range *values {
		val := xType.Deref(v)
		_, ok := r.parent.valuePosition[name][val]
		if !ok {
			r.parent.valuePosition[name][val] = make([]int, 0)
		}

		r.parent.valuePosition[name][val] = append(r.parent.valuePosition[name][val], position)
	}
}

func (r *Collector) Relations() []*Collector {
	result := make([]*Collector, len(r.view.With))
	for i := range r.view.With {
		dest := reflect.MakeSlice(r.view.With[i].Of.View.Schema.SliceType(), 0, 1).Interface()
		slice := r.view.With[i].Of.View.Schema.Slice()

		result[i] = &Collector{
			parent:          r,
			dest:            dest,
			appender:        slice.Appender(xunsafe.AsPointer(dest)),
			valuePosition:   make(map[string]map[interface{}][]int),
			types:           make(map[string]*xunsafe.Type),
			values:          make(map[string]*[]interface{}),
			slice:           slice,
			view:            &r.view.With[i].Of.View,
			relation:        r.view.With[i],
			supportParallel: r.view.With[i].Of.MatchStrategy.SupportsParallel(),
		}
	}

	r.relations = result
	return result
}

func (r *Collector) View() *View {
	return r.view
}

func (r *Collector) Dest() interface{} {
	return r.dest
}

func (r *Collector) SupportsParallel() bool {
	return r.supportParallel
}

func (r *Collector) MergeData() {
	for i := range r.relations {
		r.relations[i].MergeData()
	}

	if r.parent == nil || !r.parent.SupportsParallel() {
		return
	}

	r.mergeToParent()
}

func (r *Collector) mergeToParent() {
	valuePositions := r.parentValuesPositions(r.relation.Column)
	destPtr := xunsafe.AsPointer(r.dest)
	field := r.relation.Of.field
	holderField := r.relation.holderField
	parentSlice := r.parent.slice
	parentDestPtr := xunsafe.AsPointer(r.parent.dest)

	for i := 0; i < r.slice.Len(destPtr); i++ {
		value := r.slice.ValuePointerAt(destPtr, i)
		key := field.Value(xunsafe.AsPointer(value))
		positions, ok := valuePositions[key]
		if !ok {
			continue
		}

		for _, position := range positions {
			parentValue := parentSlice.ValuePointerAt(parentDestPtr, position)
			if r.relation.Cardinality == "One" {
				at := r.slice.ValuePointerAt(destPtr, i)
				holderField.SetValue(xunsafe.AsPointer(parentValue), at)
			} else if r.relation.Cardinality == "Many" {
				appender := r.slice.Appender(holderField.ValuePointer(xunsafe.AsPointer(parentValue)))
				appender.Append(value)
			}
		}
	}
}

func (r *Collector) ParentPlaceholders() ([]interface{}, string) {
	if r.parent == nil || r.parent.SupportsParallel() {
		return []interface{}{}, ""
	}

	positions := r.parentValuesPositions(r.relation.Column)
	result := make([]interface{}, len(positions))
	counter := 0
	for key := range positions {
		result[counter] = key
		counter++
	}

	return result, r.relation.Of.Column
}