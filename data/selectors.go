package data

import (
	"github.com/viant/xunsafe"
	"reflect"
)

//Selectors represents Selector registry
type Selectors map[string]*Selector

//Lookup returns and initializes Selector attached to View. Creates new one if doesn't exist.
func (s Selectors) Lookup(view *View) *Selector {
	selector, ok := s[view.Name]
	if !ok {
		selector = &Selector{}
		s[view.Name] = selector
	}
	selector.Parameters.Init(view)
	return selector
}

func (s *ParamState) Init(view *View) {
	if s.Values == nil {
		s.Values = newValue(view.Template.Schema.Type())
		s.Has = newValue(view.Template.PresenceSchema.Type())
	}

}

func newValue(p reflect.Type) interface{} {
	if p.Kind() == reflect.Ptr {
		return reflect.New(p.Elem()).Interface()
	}
	result := reflect.New(p)

	//initialise pointers
	for i := 0; i < p.NumField(); i++ {
		ptr := xunsafe.ValuePointer(&result)
		field := p.Field(i)
		if field.Type.Kind() == reflect.Ptr {
			newValue := reflect.New(field.Type.Elem()).Interface()
			xField := xunsafe.NewField(field)
			xField.SetValue(ptr, newValue)
		}
	}
	//if p.NumField() == 1 { //if struct has one filed, go returns value of the first field, if pointer it would return nil
	//	//to workaround we initialise value of the struct
	//	ptr := xunsafe.ValuePointer(&result)
	//	field := p.Field(0)
	//	xField := xunsafe.NewField(field)
	//	if field.Type.Kind() == reflect.Ptr {
	//		newValue := reflect.New(field.Type.Elem()).Interface()
	//		xField.SetValue(ptr, newValue)
	//	} else {
	//		xField.SetValue(ptr, reflect.New(field.Type).Elem().Interface())
	//	}
	//}
	ret := result.Elem().Interface()
	return ret
}

//Init initializes each Selector
func (s Selectors) Init() {
	for _, selector := range s {
		selector.Init()
	}
}
