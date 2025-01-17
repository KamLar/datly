package view

import (
	"context"
	"fmt"
	"reflect"
	"strings"
)

const (
	FieldsQuery   = "_fields"
	OffsetQuery   = "_offset"
	LimitQuery    = "_limit"
	CriteriaQuery = "_criteria"
	OrderByQuery  = "_orderby"
	PageQuery     = "_page"
)

var intType = reflect.TypeOf(0)
var stringType = reflect.TypeOf("")

//Config represent a view config selector
type (
	Config struct {
		//TODO: Should order by be a slice?
		Namespace     string             `json:",omitempty"`
		OrderBy       string             `json:",omitempty"`
		Limit         int                `json:",omitempty"`
		Constraints   *Constraints       `json:",omitempty"`
		Parameters    *SelectorParameter `json:",omitempty"`
		LimitParam    *Parameter         `json:",omitempty"`
		OffsetParam   *Parameter         `json:",omitempty"`
		PageParam     *Parameter         `json:",omitempty"`
		FieldsParam   *Parameter         `json:",omitempty"`
		OrderByParam  *Parameter         `json:",omitempty"`
		CriteriaParam *Parameter         `json:",omitempty"`

		limitDefault    *bool
		offsetDefault   *bool
		pageDefault     *bool
		fieldsDefault   *bool
		criteriaDefault *bool
		orderByDefault  *bool
	}

	SelectorParameter struct {
		Limit    string `json:",omitempty"`
		Offset   string `json:",omitempty"`
		Page     string `json:",omitempty"`
		Fields   string `json:",omitempty"`
		OrderBy  string `json:",omitempty"`
		Criteria string `json:",omitempty"`
	}
)

func (c *Config) ParameterName(ns, paramName string) string {
	if c.Parameters == nil {
		return ns + paramName
	}

	var result = ""
	if ns != "" && strings.HasPrefix(paramName, ns) {
		paramName = paramName[len(ns):]
	}
	switch strings.ToLower(paramName) {
	case FieldsQuery:
		result = c.Parameters.Fields
	case OffsetQuery:
		result = c.Parameters.Offset
	case OrderByQuery:
		result = c.Parameters.OrderBy
	case LimitQuery:
		result = c.Parameters.Limit
	case CriteriaQuery:
		result = c.Parameters.Criteria
	case PageQuery:
		result = c.Parameters.Page
	}
	if result == "" {
		return ns + paramName
	}
	return result
}

func (c *Config) Init(ctx context.Context, resource *Resource, parent *View) error {
	if err := c.ensureConstraints(resource); err != nil {
		return err
	}

	parameters := c.Parameters
	if parameters == nil {
		parameters = &SelectorParameter{}
	}

	if name := parameters.Limit; (name != "" || c.Constraints.Limit) && derefBool(c.limitDefault, c.LimitParam == nil) {
		c.limitDefault = boolPtr(name == "")
		c.LimitParam = c.newSelectorParam(name, LimitQuery, parent)
	}

	if name := parameters.Offset; (name != "" || c.Constraints.Offset) && derefBool(c.offsetDefault, c.OffsetParam == nil) {
		c.offsetDefault = boolPtr(name == "")
		c.OffsetParam = c.newSelectorParam(name, OffsetQuery, parent)
	}

	if name := parameters.Page; (name != "" || c.Constraints.IsPageEnabled()) && derefBool(c.pageDefault, c.PageParam == nil) {
		c.pageDefault = boolPtr(name == "")
		c.PageParam = c.newSelectorParam(name, PageQuery, parent)
	}

	if name := parameters.Fields; (name != "" || c.Constraints.Projection) && derefBool(c.fieldsDefault, c.FieldsParam == nil) {
		c.fieldsDefault = boolPtr(name == "")
		c.FieldsParam = c.newSelectorParam(name, FieldsQuery, parent)
	}

	if name := parameters.Criteria; (name != "" || c.Constraints.Criteria) && derefBool(c.criteriaDefault, c.CriteriaParam == nil) {
		c.criteriaDefault = boolPtr(name == "")
		c.CriteriaParam = c.newSelectorParam(name, CriteriaQuery, parent)
	}

	if name := parameters.OrderBy; (name != "" || c.Constraints.OrderBy) && derefBool(c.orderByDefault, c.OrderByParam == nil) {
		c.orderByDefault = boolPtr(name == "")
		c.OrderByParam = c.newSelectorParam(name, OrderByQuery, parent)
	}

	if err := c.initCustomParams(ctx, resource, parent); err != nil {
		return err
	}

	return nil
}

func (c *Config) newSelectorParam(name, paramKind string, parent *View) *Parameter {
	return &Parameter{
		Name:        FirstNotEmpty(name, paramKind),
		In:          NewQueryLocation(FirstNotEmpty(name, c.Namespace+paramKind)),
		Schema:      NewSchema(ParamType(paramKind)),
		Description: Description(paramKind, parent.Name),
	}
}

func (c *Config) Clone() *Config {
	if c == nil {
		return nil
	}

	result := *c
	return &result
}

func (c *Config) ensureConstraints(resource *Resource) error {
	if c.Constraints == nil {
		c.Constraints = &Constraints{}
	}

	return c.Constraints.init(resource)
}

func (c *Config) initCustomParams(ctx context.Context, resource *Resource, parent *View) error {
	if err := c.initParamIfNeeded(ctx, c.CriteriaParam, resource, stringType, parent); err != nil {
		return err
	}

	if err := c.initParamIfNeeded(ctx, c.LimitParam, resource, intType, parent); err != nil {
		return err
	}

	if err := c.initParamIfNeeded(ctx, c.OrderByParam, resource, stringType, parent); err != nil {
		return err
	}

	if err := c.initParamIfNeeded(ctx, c.OffsetParam, resource, intType, parent); err != nil {
		return err
	}

	if err := c.initParamIfNeeded(ctx, c.FieldsParam, resource, stringType, parent); err != nil {
		return err
	}

	if err := c.initParamIfNeeded(ctx, c.PageParam, resource, intType, parent); err != nil {
		return err
	}

	return nil
}

func (c *Config) initParamIfNeeded(ctx context.Context, param *Parameter, resource *Resource, requiredType reflect.Type, view *View) error {
	if param == nil {
		return nil
	}

	if err := param.Init(ctx, view, resource, nil); err != nil {
		return err
	}

	if param.Schema.Type() != requiredType {
		return fmt.Errorf("parameter %v type missmatch, required parameter to be type of %v but was %v", param.Name, requiredType.String(), param.Schema.Type().String())
	}

	return nil
}

func (c *Config) CloneWithNs(ctx context.Context, resource *Resource, owner *View, ns string) (*Config, error) {
	shallowCopy := *c
	shallowCopy.Namespace = ns
	copyRef := &shallowCopy
	return copyRef, copyRef.Init(ctx, resource, owner)
}

func ParamType(name string) reflect.Type {
	switch name {
	case LimitQuery, OffsetQuery, PageQuery:
		return intType
	default:
		return stringType
	}
}

func Description(paramName, viewName string) string {
	switch paramName {
	case LimitQuery:
		return fmt.Sprintf("allows to limit %v view data returned from db", viewName)
	case OffsetQuery:
		return fmt.Sprintf("allows to skip first n  view %v records, it has to be used alongside the limit", viewName)
	case CriteriaQuery:
		return fmt.Sprintf("allows to filter view %v data that matches given criteria", viewName)
	case FieldsQuery:
		return fmt.Sprintf("allows to control view %v fields present in response", viewName)
	case OrderByQuery:
		return fmt.Sprintf("allows to sort view %v results", viewName)
	case PageQuery:
		return fmt.Sprintf("allows to skip first page * limit values, starting from 1 page. Has precedence over offset")
	}

	return ""
}

func boolPtr(value bool) *bool {
	return &value
}

func derefBool(value *bool, onNil bool) bool {
	if value == nil {
		return onNil
	}

	return *value
}
