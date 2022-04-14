package router

import (
	"context"
	"github.com/viant/afs"
	"github.com/viant/datly/data"
	"github.com/viant/toolbox"
	"gopkg.in/yaml.v3"
)

type Resource struct {
	Routes     Routes
	Resource   *data.Resource
	ViewPrefix map[string]string
}

func (r *Resource) Init(ctx context.Context) error {
	if err := r.Resource.Init(ctx); err != nil {
		return err
	}

	for _, route := range r.Routes {
		if err := route.View.Init(ctx, r.Resource); err != nil {
			return err
		}
	}

	if r.ViewPrefix == nil {
		r.ViewPrefix = map[string]string{}
	}

	return nil
}

func NewResourceFromURL(ctx context.Context, url string) (*Resource, error) {
	fs := afs.New()
	resourceData, err := fs.DownloadWithURL(ctx, url)
	if err != nil {
		return nil, err
	}

	transient := map[string]interface{}{}
	if err := yaml.Unmarshal(resourceData, &transient); err != nil {
		return nil, err
	}

	aMap := map[string]interface{}{}
	if err := yaml.Unmarshal(resourceData, &aMap); err != nil {
		return nil, err
	}

	resource := &Resource{}
	err = toolbox.DefaultConverter.AssignConverted(resource, aMap)
	if err != nil {
		return nil, err
	}

	if err := resource.Init(ctx); err != nil {
		return nil, err
	}

	return resource, err
}
