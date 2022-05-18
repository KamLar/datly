package view

import (
	"context"
	"fmt"
	"github.com/viant/afs"
	"github.com/viant/afs/url"
	"github.com/viant/datly/logger"
	"github.com/viant/datly/visitor"
	"github.com/viant/toolbox"
	"gopkg.in/yaml.v3"
)

//Resource represents grouped view needed to build the View
//can be loaded from i.e. yaml file
type Resource struct {
	Metrics     *Metrics
	SourceURL   string
	Connectors  []*Connector
	_connectors Connectors

	Views  []*View
	_views Views

	Parameters  []*Parameter
	_parameters ParametersIndex

	Types  []*Definition
	_types Types

	Loggers  logger.Adapters
	_loggers logger.AdapterIndex

	_visitors visitor.Visitors
}

func (r *Resource) LoadText(ctx context.Context, URL string) (string, error) {
	if url.Scheme(URL, "empty") == "empty" && r.SourceURL != "" {
		URL = url.Join(r.SourceURL, URL)
	}
	fs := afs.New()
	data, err := fs.DownloadWithURL(ctx, URL)
	return string(data), err

}

func (r *Resource) MergeFrom(resource *Resource, types Types) {
	r.mergeViews(resource)
	r.mergeParameters(resource)
	r.mergeTypes(resource, types)
}

func (r *Resource) mergeViews(resource *Resource) {
	if len(resource.Views) == 0 {
		return
	}
	views := r.viewByName()
	for i, candidate := range resource.Views {
		if _, ok := views[candidate.Name]; !ok {
			view := *resource.Views[i]
			r.Views = append(r.Views, &view)
		}
	}
}

func (r *Resource) mergeParameters(resource *Resource) {
	if len(resource.Parameters) == 0 {
		return
	}
	views := r.paramByName()
	for i, candidate := range resource.Parameters {
		if _, ok := views[candidate.Name]; !ok {
			param := *resource.Parameters[i]
			r.Parameters = append(r.Parameters, &param)
		}
	}
}

func (r *Resource) mergeTypes(resource *Resource, types Types) {
	if len(resource.Types) == 0 {
		return
	}
	views := r.typeByName()
	for i, candidate := range resource.Types {
		if _, ok := types[candidate.Name]; ok {
			continue
		}
		if _, ok := views[candidate.Name]; !ok {
			typeDef := *resource.Types[i]
			r.Types = append(r.Types, &typeDef)
		}
	}
}

func (r *Resource) viewByName() map[string]*View {
	index := map[string]*View{}
	if len(r.Views) == 0 {
		return index
	}
	for i, view := range r.Views {
		index[view.Name] = r.Views[i]
	}
	return index
}

func (r *Resource) paramByName() map[string]*Parameter {
	index := map[string]*Parameter{}
	if len(r.Parameters) == 0 {
		return index
	}
	for i, param := range r.Parameters {
		index[param.Name] = r.Parameters[i]
	}
	return index
}

func (r *Resource) typeByName() map[string]*Definition {
	index := map[string]*Definition{}
	if len(r.Parameters) == 0 {
		return index
	}
	for i, param := range r.Types {
		index[param.Name] = r.Types[i]
	}
	return index
}

//GetViews returns Views supplied with the Resource
func (r *Resource) GetViews() Views {
	if len(r._views) == 0 {
		r._views = Views{}
		for i, view := range r.Views {
			r._views[view.Name] = r.Views[i]
		}
	}
	return r._views
}

//GetConnectors returns Connectors supplied with the Resource
func (r *Resource) GetConnectors() Connectors {
	if len(r.Connectors) == 0 {
		r._connectors = Connectors{}
		for i, connector := range r.Connectors {
			r._connectors[connector.Name] = r.Connectors[i]
		}
	}
	return r._connectors
}

//Init initializes Resource
func (r *Resource) Init(ctx context.Context, types Types, visitors visitor.Visitors) error {
	r._types = types.copy()
	r._visitors = visitors

	for _, definition := range r.Types {
		if err := definition.Init(ctx, types); err != nil {
			return err
		}

		_, err := r._types.Lookup(definition.Name)
		if err == nil {
			return fmt.Errorf("%v type is already registered", definition.Name)
		}

		r._types.Register(definition.Name, definition.Type())
	}

	r._views = ViewSlice(r.Views).Index()
	r._connectors = ConnectorSlice(r.Connectors).Index()
	r._parameters = ParametersSlice(r.Parameters).Index()
	r._loggers = r.Loggers.Index()

	if err := ConnectorSlice(r.Connectors).Init(ctx, r._connectors); err != nil {
		return err
	}

	if err := ViewSlice(r.Views).Init(ctx, r); err != nil {
		return err
	}

	return nil
}

//View returns View with given name
func (r *Resource) View(name string) (*View, error) {
	return r._views.Lookup(name)
}

//NewResourceFromURL loads and initializes Resource from file .yaml
func NewResourceFromURL(ctx context.Context, url string, types Types, visitors visitor.Visitors) (*Resource, error) {
	resource, err := LoadResourceFromURL(ctx, url, afs.New())
	if err != nil {
		return nil, err
	}
	err = resource.Init(ctx, types, visitors)
	return resource, err
}

//LoadResourceFromURL load resource from URL
func LoadResourceFromURL(ctx context.Context, URL string, fs afs.Service) (*Resource, error) {
	data, err := fs.DownloadWithURL(ctx, URL)
	if err != nil {
		return nil, err
	}
	transient := map[string]interface{}{}
	if err := yaml.Unmarshal(data, &transient); err != nil {
		return nil, err
	}
	aMap := map[string]interface{}{}
	if err := yaml.Unmarshal(data, &aMap); err != nil {
		return nil, err
	}
	resource := &Resource{}
	err = toolbox.DefaultConverter.AssignConverted(resource, aMap)
	if err != nil {
		return nil, err
	}
	resource.SourceURL = URL
	return resource, err
}

func (r *Resource) FindConnector(view *View) (*Connector, error) {
	if view.Connector == nil {
		var connector *Connector

		for _, relView := range r.Views {
			if relView.Name == view.Name {
				continue
			}

			if isChildOfAny(view, relView.With) {
				connector = relView.Connector
				break
			}
		}

		if connector != nil {
			result := *connector
			return &result, nil
		}
	}

	if view.Connector != nil {
		if view.Connector.Ref != "" {
			return r._connectors.Lookup(view.Connector.Ref)
		}

		if err := view.Connector.Validate(); err == nil {
			return view.Connector, nil
		}
	}

	return nil, fmt.Errorf("couldn't inherit connector for view %v from any other parent views", view.Name)
}

func isChildOfAny(view *View, with []*Relation) bool {
	for _, relation := range with {
		if relation.Of.View.Ref == view.Name {
			return true
		}

		if isChildOfAny(view, relation.Of.With) {
			return true
		}
	}

	return false
}

func EmptyResource() *Resource {
	return &Resource{
		Connectors:  make([]*Connector, 0),
		_connectors: Connectors{},
		Views:       make([]*View, 0),
		_views:      Views{},
		Parameters:  make([]*Parameter, 0),
		_parameters: ParametersIndex{},
		_types:      Types{},
	}
}

//NewResource creates a Resource and register provided Types
func NewResource(types Types) *Resource {
	return &Resource{_types: types}
}

//AddViews register views in the resource
func (r *Resource) AddViews(views ...*View) {
	if r.Views == nil {
		r.Views = make([]*View, 0)
	}

	r.Views = append(r.Views, views...)
}

//AddConnectors register connectors in the resource
func (r *Resource) AddConnectors(connectors ...*Connector) {
	if r.Connectors == nil {
		r.Connectors = make([]*Connector, 0)
	}

	r.Connectors = append(r.Connectors, connectors...)
}

//AddParameters register parameters in the resource
func (r *Resource) AddParameters(parameters ...*Parameter) {
	if r.Parameters == nil {
		r.Parameters = make([]*Parameter, 0)
	}

	r.Parameters = append(r.Parameters, parameters...)
}

//AddLoggers register loggers in the resource
func (r *Resource) AddLoggers(loggers ...*logger.Adapter) {
	r.Loggers = append(r.Loggers, loggers...)
}

func (r *Resource) SetTypes(types Types) {
	r._types = types
}

func (r *Resource) GetTypes() Types {
	return r._types
}