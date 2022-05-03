package data

import (
	"context"
	"fmt"
	"github.com/viant/datly/shared"
	"github.com/viant/velty"
	"github.com/viant/velty/est"
	"github.com/viant/xunsafe"
	"reflect"
)

var boolType = reflect.TypeOf(true)

const (
	paramsMetadataKey = "Has"
	paramsKey         = "Unsafe"

	Pagination = "$PAGINATION"
	Criteria   = "$CRITERIA"
	ColumnsIn  = "$COLUMN_IN"
)

type (
	Template struct {
		Source string

		Schema         *Schema
		PresenceSchema *Schema

		Parameters []*Parameter

		criteriaEvaluator *Evaluator
		sqlEvaluator      *Evaluator

		_fields          []reflect.StructField
		_fieldIndex      map[string]int
		_parametersIndex ParametersIndex

		initialized bool
	}

	Evaluator struct {
		planner       *velty.Planner
		executor      *est.Execution
		stateProvider func() *est.State
		parameterized bool
	}

	CommonParams struct {
		ColumnsIn   string `velty:"COLUMN_IN"`
		WhereClause string `velty:"CRITERIA"`
		Pagination  string `velty:"PAGINATION"`
	}

	paramsPosition struct {
		columnsIn      bool
		criteria       bool
		pagination     bool
		hasWhereClause bool
	}
)

func (t *Template) Init(ctx context.Context, resource *Resource, view *View) error {
	if t.initialized {
		return nil
	}

	t.initialized = true
	t._parametersIndex = ParametersSlice(t.Parameters).Index()
	t._fieldIndex = map[string]int{}

	if t.Source != "" {
		t.Source = "( " + t.Source + " )"
	} else {
		t.Source = view.Source()
	}

	if err := t.initTypes(ctx, resource); err != nil {
		return err
	}

	if err := t.initPresenceType(); err != nil {
		return err
	}

	if err := t.initCriteriaEvaluator(view); err != nil {
		return err
	}

	if err := t.initSqlEvaluator(); err != nil {
		return err
	}

	if err := t.updateParametersFields(); err != nil {
		return err
	}

	return nil
}

func (t *Template) initTypes(ctx context.Context, resource *Resource) error {
	if t.Schema == nil || (t.Schema.Name == "" && t.Schema.Type() == nil) {
		return t.createSchemaFromParams(ctx, resource)
	}

	return t.inheritParamTypesFromSchema(ctx, resource)
}

func (t *Template) createSchemaFromParams(ctx context.Context, resource *Resource) error {
	t.Schema = &Schema{}

	for _, param := range t.Parameters {
		if err := t.inheritAndInitParam(ctx, resource, param); err != nil {
			return err
		}

		if err := t.addField(param.Name, param.Schema.Type()); err != nil {
			return err
		}
	}

	structType := reflect.StructOf(t._fields)
	t.Schema.setType(structType)

	return nil
}

func (t *Template) ParamByName(name string) (*Parameter, error) {
	return t._parametersIndex.Lookup(name)
}

func (t *Template) addField(name string, rType reflect.Type) error {
	_, ok := t._fieldIndex[name]
	if ok {
		return fmt.Errorf("field with %v name already exists", name)
	}

	field, err := TemplateField(name, rType)
	if err != nil {
		return err
	}

	t._fieldIndex[name] = len(t._fields)
	t._fields = append(t._fields, field)

	return nil
}

func TemplateField(name string, rType reflect.Type) (reflect.StructField, error) {
	if len(name) == 0 {
		return reflect.StructField{}, fmt.Errorf("template field name can't be empty")
	}

	pkgPath := ""
	if name[0] < 'A' || name[0] > 'Z' {
		pkgPath = "github.com/viant/datly/router"
	}

	field := reflect.StructField{Name: name, Type: rType, PkgPath: pkgPath}
	return field, nil
}

func (t *Template) inheritParamTypesFromSchema(ctx context.Context, resource *Resource) error {
	if t.Schema.Type() == nil {
		rType, err := resource.types.Lookup(t.Schema.Name)
		if err != nil {
			return err
		}
		t.Schema.setType(rType)
	}

	if t.Schema.Type().Kind() == reflect.Ptr {
		return fmt.Errorf("params schema %v type can't be a pointer", t.Schema.Name)
	}

	for _, parameter := range t.Parameters {
		if err := t.inheritAndInitParam(ctx, resource, parameter); err != nil {
			return err
		}

		field, err := fieldByTemplateName(t.Schema.Type(), parameter.Name)
		if err != nil {
			return err
		}

		if err := parameter.Init(ctx, resource, field); err != nil {
			return err
		}
	}

	return nil
}

func (t *Template) newEvaluator(template string, parameterized bool) (*Evaluator, error) {
	evaluator := &Evaluator{
		parameterized: parameterized,
	}
	var err error

	evaluator.planner = velty.New(velty.BufferSize(len(template)))
	if parameterized {
		if err = evaluator.planner.DefineVariable(paramsKey, t.Schema.Type()); err != nil {
			return nil, err
		}

		if err = evaluator.planner.DefineVariable(paramsMetadataKey, t.PresenceSchema.Type()); err != nil {
			return nil, err
		}
	}

	evaluator.executor, evaluator.stateProvider, err = evaluator.planner.Compile([]byte(template))
	if err != nil {
		return nil, err
	}

	return evaluator, nil
}

func (t *Template) EvaluateCriteria(externalParams, presenceMap interface{}) (string, error) {
	return t.evaluate(t.criteriaEvaluator, externalParams, presenceMap)
}

func (t *Template) EvaluateSource(externalParams, presenceMap interface{}) (string, error) {
	return t.evaluate(t.sqlEvaluator, externalParams, presenceMap)
}

func (t *Template) evaluate(evaluator *Evaluator, externalParams, presenceMap interface{}) (string, error) {
	if t.Schema.Type() != reflect.TypeOf(externalParams) {
		return "", fmt.Errorf("inompactible types, wanted %v got %T", t.Schema.Type().String(), externalParams)
	}

	newState := evaluator.stateProvider()
	if evaluator.parameterized {
		if externalParams != nil {
			if err := newState.SetValue(paramsKey, externalParams); err != nil {
				return "", err
			}
		}

		if presenceMap != nil {
			if err := newState.SetValue(paramsMetadataKey, presenceMap); err != nil {
				return "", err
			}
		}
	}

	evaluator.executor.Exec(newState)
	return newState.Buffer.String(), nil
}

func (t *Template) initCriteriaEvaluator(view *View) error {
	if view.Criteria == "" {
		return nil
	}

	evaluator, err := t.newEvaluator(view.Criteria, true)
	if err != nil {
		return err
	}

	t.criteriaEvaluator = evaluator
	return nil
}

func (t *Template) inheritAndInitParam(ctx context.Context, resource *Resource, param *Parameter) error {
	if param.Ref == "" {
		return param.Init(ctx, resource, nil)
	}

	paramRef, err := resource._parameters.Lookup(param.Ref)
	if err != nil {
		return err
	}

	if err = paramRef.Init(ctx, resource, nil); err != nil {
		return err
	}

	param.inherit(paramRef)
	return nil
}

func (t *Template) initSqlEvaluator() error {
	evaluator, err := t.newEvaluator(t.Source, true)
	if err != nil {
		return err
	}

	t.sqlEvaluator = evaluator
	return nil
}

func (t *Template) initPresenceType() error {
	if t.PresenceSchema == nil {
		return t.initPresenceSchemaFromParams()
	}

	return nil
}

func (t *Template) initPresenceSchemaFromParams() error {
	var err error
	fields := make([]reflect.StructField, len(t.Parameters))
	for i, parameter := range t.Parameters {
		fields[i], err = TemplateField(parameter.Name, boolType)
		if err != nil {
			return err
		}
	}

	t.PresenceSchema = &Schema{}
	t.PresenceSchema.setType(reflect.StructOf(fields))

	return nil
}

func (t *Template) updateParametersFields() error {
	for _, parameter := range t.Parameters {
		presenceField, err := fieldByTemplateName(t.PresenceSchema.Type(), parameter.PresenceName)
		if err != nil {
			return err
		}

		parameter.presenceXfield = presenceField
		field, err := fieldByTemplateName(t.Schema.Type(), parameter.Name)
		if err != nil {
			return err
		}

		parameter.xfield = field
	}

	return nil
}

func fieldByTemplateName(structType reflect.Type, name string) (*xunsafe.Field, error) {
	structType = shared.Elem(structType)

	field, ok := structType.FieldByName(name)
	if !ok {
		for i := 0; i < structType.NumField(); i++ {
			field = structType.Field(i)
			veltyTag := velty.Parse(field.Tag.Get("velty"))
			for _, fieldName := range veltyTag.Names {
				if fieldName == name {
					return xunsafe.NewField(field), nil
				}
			}
		}

		return nil, fmt.Errorf("not found field %v at type %v", name, structType.String())
	}

	return xunsafe.NewField(field), nil
}
