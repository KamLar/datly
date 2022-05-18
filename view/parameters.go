package view

import (
	"context"
	"fmt"
	"github.com/viant/datly/shared"
	"github.com/viant/datly/visitor"
	"github.com/viant/xunsafe"
	"reflect"
	"strings"
	"unsafe"
)

type (
	//Parameter describes parameters used by the Criteria to filter the view.
	Parameter struct {
		shared.Reference
		Name         string
		PresenceName string

		In          *Location
		Required    *bool
		Description string
		Style       string
		Schema      *Schema

		Codec *Codec

		initialized bool
		view        *View

		valueAccessor    *Accessor
		presenceAccessor *Accessor
	}

	//Location tells how to get parameter value.
	Location struct {
		Kind Kind
		Name string
	}

	CodecFn func(context context.Context, rawValue string) (interface{}, error)
	Codec   struct {
		Name       string
		_visitorFn CodecFn //shall rename to codec ?
	}
)

func (v *Codec) Init(resource *Resource, paramType reflect.Type) error {
	vVisitor, err := resource._visitors.Lookup(v.Name)
	if err != nil {
		return err
	}

	switch actual := vVisitor.Visitor().(type) {
	case visitor.Codec:
		v._visitorFn = actual.Value
		return nil
	default:
		return fmt.Errorf("expected %T to implement Codec", actual)
	}
}

//Init initializes Parameter
func (p *Parameter) Init(ctx context.Context, resource *Resource, structType reflect.Type) error {
	if p.initialized == true {
		return nil
	}
	p.initialized = true

	if p.Ref != "" {
		param, err := resource._parameters.Lookup(p.Ref)
		if err != nil {
			return err
		}

		if err = param.Init(ctx, resource, structType); err != nil {
			return err
		}

		p.inherit(param)
	}
	if p.PresenceName == "" {
		p.PresenceName = p.Name
	}

	if p.In.Kind == DataViewKind {
		view, err := resource.View(p.In.Name)
		if err != nil {
			return fmt.Errorf("failed to lookup parameter %v view %w", p.Name, err)
		}

		if err = view.Init(ctx, resource); err != nil {
			return err
		}

		p.view = view
	}

	if err := p.initSchema(resource._types, structType); err != nil {
		return err
	}

	if err := p.initVisitors(resource); err != nil {
		return err
	}

	return p.Validate()
}

func (p *Parameter) inherit(param *Parameter) {
	p.Name = notEmptyOf(p.Name, param.Name)
	p.Description = notEmptyOf(p.Description, param.Description)
	p.Style = notEmptyOf(p.Style, param.Style)
	p.PresenceName = notEmptyOf(p.PresenceName, param.PresenceName)

	if p.In == nil {
		p.In = param.In
	}

	if p.Required == nil {
		p.Required = param.Required
	}

	if p.Schema == nil {
		p.Schema = param.Schema.copy()
	}

	if p.Codec == nil {
		p.Codec = param.Codec
	}

	if p.view == nil {
		p.view = param.view
	}
}

//Validate checks if parameter is valid
func (p *Parameter) Validate() error {
	if p.Name == "" {
		return fmt.Errorf("parameter name can't be empty")
	}

	if p.In == nil {
		return fmt.Errorf("parameter location can't be empty")
	}

	if err := p.In.Validate(); err != nil {
		return err
	}

	return nil
}

//View returns View related with Parameter if Location.Kind is set to data_view
func (p *Parameter) View() *View {
	return p.view
}

//Validate checks if Location is valid
func (l *Location) Validate() error {
	if err := l.Kind.Validate(); err != nil {
		return err
	}

	if err := ParamName(l.Name).Validate(l.Kind); err != nil {
		return fmt.Errorf("unsupported param name")
	}

	return nil
}

func (p *Parameter) IsRequired() bool {
	return p.Required != nil && *p.Required == true
}

func (p *Parameter) initSchema(types Types, structType reflect.Type) error {
	if structType != nil {
		return p.initSchemaFromType(structType)
	}

	if p.Schema == nil {
		return fmt.Errorf("parameter %v schema can't be empty", p.Name)
	}

	if p.Schema.DataType == "" && p.Schema.Name == "" {
		return fmt.Errorf("parameter %v either schema DataType or Name has to be specified", p.Name)
	}

	if p.Schema.Name != "" {
		lookup, err := types.Lookup(p.Schema.Name)
		if err != nil {
			return err
		}

		p.Schema.setType(lookup)
		return nil
	}

	if p.Schema.DataType != "" {
		rType, err := parseType(p.Schema.DataType)
		if err != nil {
			return err
		}
		p.Schema.setType(rType)
		return nil
	}

	return p.Schema.Init(nil, nil, 0, types)
}

func (p *Parameter) initSchemaFromType(structType reflect.Type) error {
	if p.Schema == nil {
		p.Schema = &Schema{}
	}

	segments := strings.Split(p.Name, ".")
	field, err := fieldByTemplateName(structType, segments[0])
	if err != nil {
		return err
	}

	p.Schema.setType(field.Type)
	return nil
}

func (p *Parameter) UpdatePresence(presencePtr unsafe.Pointer) {
	p.presenceAccessor.setBool(presencePtr, true)
}

func (p *Parameter) SetAccessor(accessor *Accessor) {
	p.valueAccessor = accessor
}

func (p *Parameter) pathFields(path string, structType reflect.Type) ([]*xunsafe.Field, error) {
	segments := strings.Split(path, ".")
	if len(segments) == 0 {
		return nil, fmt.Errorf("path can't be empty")
	}

	xFields := make([]*xunsafe.Field, len(segments))

	xField, err := fieldByTemplateName(structType, segments[0])
	if err != nil {
		return nil, err
	}

	xFields[0] = xField
	for i := 1; i < len(segments); i++ {
		newField, err := fieldByTemplateName(xFields[i-1].Type, segments[i])
		if err != nil {
			return nil, err
		}
		xFields[i] = newField
	}
	return xFields, nil
}

func (p *Parameter) Value(values interface{}) (interface{}, error) {
	return p.valueAccessor.Value(values)
}

func (p *Parameter) ConvertAndSet(ctx context.Context, paramPtr unsafe.Pointer, value string) error {
	return p.valueAccessor.setValue(ctx, paramPtr, value, p.Codec)
}

func elem(rType reflect.Type) reflect.Type {
	for rType.Kind() == reflect.Ptr {
		rType = rType.Elem()
	}

	return rType
}

func (p *Parameter) Set(ptr unsafe.Pointer, value interface{}) error {
	p.valueAccessor.set(ptr, value)
	return nil
}

func (p *Parameter) SetPresenceField(structType reflect.Type) error {
	fields, err := p.pathFields(p.PresenceName, structType)
	if err != nil {
		return err
	}

	p.presenceAccessor = &Accessor{
		xFields: fields,
	}

	return nil
}

func (p *Parameter) initVisitors(resource *Resource) error {
	if err := p.initCodec(resource); err != nil {
		return err
	}
	return nil
}

func (p *Parameter) initCodec(resource *Resource) error {
	if p.Codec == nil {
		return nil
	}

	if err := p.Codec.Init(resource, p.Schema.Type()); err != nil {
		return err
	}
	return nil
}