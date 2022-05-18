package router

import (
	"context"
	"fmt"
	"github.com/viant/datly/data"
	"github.com/viant/datly/reader"
	"github.com/viant/datly/sanitize"
	"github.com/viant/datly/shared"
	"github.com/viant/xunsafe"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"unsafe"
)

type RequestMetadata struct {
	URI      string
	Index    Index
	MainView *data.View
}

func CreateSelectorsFromRoute(ctx context.Context, route *Route, request *http.Request, views ...*data.View) (data.Selectors, error) {
	requestMetadata := &RequestMetadata{
		URI:      route.URI,
		Index:    route.Index,
		MainView: route.View,
	}

	requestParams, err := NewRequestParameters(request, route)
	if err != nil {
		return nil, err
	}

	return CreateSelectors(ctx, requestMetadata, requestParams, views...)
}

func CreateSelectors(ctx context.Context, requestMetadata *RequestMetadata, requestParams *RequestParams, views ...*data.View) (data.Selectors, error) {
	selectors := data.Selectors{}

	if err := buildParameters(ctx, requestMetadata, &selectors, views, requestParams); err != nil {
		return nil, err
	}

	for paramName, paramValue := range requestParams.queryIndex {
		paramName = strings.ToLower(paramName)

		switch paramName {
		case string(Fields):
			if err := buildFields(&selectors, requestMetadata, paramValue[0]); err != nil {
				return nil, err
			}

		case string(Offset):
			if err := buildOffset(&selectors, requestMetadata, paramValue[0]); err != nil {
				return nil, err
			}

		case string(Limit):
			if err := buildLimit(&selectors, requestMetadata, paramValue[0]); err != nil {
				return nil, err
			}

		case string(OrderBy):
			if err := buildOrderBy(&selectors, requestMetadata, paramValue[0]); err != nil {
				return nil, err
			}

		case string(Criteria):
			if err := buildCriteria(&selectors, requestMetadata, paramValue[0]); err != nil {
				return nil, err
			}
		}
	}

	return selectors, nil
}

func buildParameters(ctx context.Context, requestMetadata *RequestMetadata, selectors *data.Selectors, views []*data.View, requestParams *RequestParams) error {
	wg := sync.WaitGroup{}
	errors := shared.NewErrors(0)
	for _, view := range views {
		if view.Template == nil || len(view.Template.Parameters) == 0 {
			continue
		}

		wg.Add(1)
		go func(view *data.View, requestMetadata *RequestMetadata) {
			defer wg.Done()
			selector := selectors.Lookup(view)
			selector.Parameters.Init(view)
			params := &selector.Parameters
			if err := buildSelectorParameters(ctx, view, xunsafe.AsPointer(params.Values), xunsafe.AsPointer(params.Has), view.Template.Parameters, requestParams, requestMetadata); err != nil {
				errors.Append(err)
			}
		}(view, requestMetadata)
	}

	wg.Wait()
	return errors.Error()
}

func buildSelectorParameters(ctx context.Context, parent *data.View, paramsPtr, presencePtr unsafe.Pointer, parameters []*data.Parameter, requestParams *RequestParams, requestMetadata *RequestMetadata) error {
	var err error
	for _, parameter := range parameters {
		switch parameter.In.Kind {
		case data.QueryKind:
			if err = addQueryParam(ctx, paramsPtr, presencePtr, parameter, requestParams); err != nil {
				return err
			}

		case data.PathKind:
			if err = addPathParam(ctx, paramsPtr, presencePtr, parameter, requestParams); err != nil {
				return err
			}

		case data.HeaderKind:
			if err = addHeaderParam(ctx, paramsPtr, presencePtr, parameter, requestParams); err != nil {
				return err
			}

		case data.CookieKind:
			if err = addCookieParam(ctx, paramsPtr, presencePtr, parameter, requestParams); err != nil {
				return err
			}

		case data.DataViewKind:
			if err = addViewParam(ctx, parent, paramsPtr, presencePtr, parameter, requestParams, requestMetadata); err != nil {
				return err
			}

		case data.RequestBodyKind:
			if err = addRequestBodyParam(paramsPtr, presencePtr, parameter, requestParams); err != nil {
				return err
			}
		}
	}
	return nil
}

func addRequestBodyParam(paramsPtr unsafe.Pointer, presencePtr unsafe.Pointer, param *data.Parameter, requestParams *RequestParams) error {
	if err := param.Set(paramsPtr, requestParams.requestBody); err != nil {
		return err
	}

	param.UpdatePresence(presencePtr)
	return nil
}

func addCookieParam(ctx context.Context, ptr unsafe.Pointer, presencePtr unsafe.Pointer, parameter *data.Parameter, params *RequestParams) error {
	return convertAndSet(ctx, ptr, presencePtr, parameter, params.cookie(parameter.In.Name))
}

func addHeaderParam(ctx context.Context, ptr unsafe.Pointer, presencePtr unsafe.Pointer, parameter *data.Parameter, params *RequestParams) error {
	return convertAndSet(ctx, ptr, presencePtr, parameter, params.header(parameter.In.Name))
}

func addQueryParam(ctx context.Context, ptr unsafe.Pointer, presencePtr unsafe.Pointer, parameter *data.Parameter, params *RequestParams) error {
	return convertAndSet(ctx, ptr, presencePtr, parameter, params.queryParam(parameter.In.Name, ""))
}

func addPathParam(ctx context.Context, ptr unsafe.Pointer, presencePtr unsafe.Pointer, parameter *data.Parameter, params *RequestParams) error {
	return convertAndSet(ctx, ptr, presencePtr, parameter, params.pathVariable(parameter.In.Name, ""))
}

func addViewParam(ctx context.Context, parent *data.View, paramsPtr, presencePtr unsafe.Pointer, param *data.Parameter, params *RequestParams, requestMetadata *RequestMetadata) error {
	view := param.View()
	destSlice := reflect.New(view.Schema.SliceType()).Interface()
	session := reader.NewSession(destSlice, view)
	session.Parent = parent
	newIndex := Index{}
	if err := newIndex.Init(view, ""); err != nil {
		return err
	}

	newRequestMetadata := &RequestMetadata{
		URI:      requestMetadata.URI,
		Index:    newIndex,
		MainView: nil,
	}

	selectors, err := CreateSelectors(ctx, newRequestMetadata, params, view)
	if err != nil {
		return err
	}

	session.Selectors = selectors
	if err = reader.New().Read(ctx, session); err != nil {
		return err
	}
	ptr := xunsafe.AsPointer(destSlice)
	paramLen := view.Schema.Slice().Len(ptr)
	switch paramLen {
	case 0:
		if param.Required != nil && *param.Required {
			return fmt.Errorf("parameter %v value is required but no data was found", param.Name)
		}
	case 1:
		holder := view.Schema.Slice().ValuePointerAt(ptr, 0)
		if err = param.Set(paramsPtr, holder); err != nil {
			return err
		}

		param.UpdatePresence(presencePtr)
		return nil

	default:
		return fmt.Errorf("parameter %v return more than one value", param.Name)
	}

	return nil
}

func convertAndSet(ctx context.Context, paramPtr, presencePtr unsafe.Pointer, parameter *data.Parameter, rawValue string) error {
	if parameter.IsRequired() && rawValue == "" {
		return fmt.Errorf("parameter %v is required", parameter.Name)
	}

	if rawValue == "" {
		return nil
	}

	if err := parameter.ConvertAndSet(ctx, paramPtr, rawValue); err != nil {
		return err
	}

	parameter.UpdatePresence(presencePtr)
	return nil
}

func buildFields(selectors *data.Selectors, requestMetadata *RequestMetadata, fieldsQuery string) error {
	fieldIt := NewParamIt(fieldsQuery)
	for fieldIt.Has() {
		param, err := fieldIt.Next()
		if err != nil {
			return err
		}

		aView, ok := paramView(param, requestMetadata)
		if !ok {
			continue
		}

		if err = canUseColumn(aView, param.Value); err != nil {
			return err
		}

		selector := selectors.Lookup(aView)
		selector.Columns = append(selector.Columns, param.Value)
	}

	return nil
}

func paramView(param Param, requestMetadata *RequestMetadata) (*data.View, bool) {
	if param.Prefix == "" {
		return requestMetadata.MainView, requestMetadata.MainView != nil
	}

	view, _ := viewByPrefix(param.Prefix, requestMetadata)
	return view, view != nil
}

func viewByPrefix(prefix string, requestMetadata *RequestMetadata) (*data.View, error) {
	return requestMetadata.Index.ViewByPrefix(prefix)
}

func canUseColumn(view *data.View, columnName string) error {
	column, ok := view.ColumnByName(columnName)
	if !ok {
		return fmt.Errorf("not found column %v in view %v", columnName, view.Name)
	}

	if !column.Filterable {
		return fmt.Errorf("column %v is not filterable", columnName)
	}

	return nil
}

func buildOffset(selectors *data.Selectors, requestMetadata *RequestMetadata, offsetQuery string) error {
	fieldIt := NewParamIt(offsetQuery)
	for fieldIt.Has() {
		param, err := fieldIt.Next()
		if err != nil {
			return err
		}

		aView, ok := paramView(param, requestMetadata)
		if !ok {
			continue
		}

		if !aView.CanUseSelectorOffset() {
			return fmt.Errorf("can't use selector offset on %v view", requestMetadata.MainView.Name)
		}

		if err = updateSelectorOffset(selectors, param.Value, requestMetadata.MainView); err != nil {
			return err
		}
	}

	return nil
}

func updateSelectorOffset(selectors *data.Selectors, offset string, view *data.View) error {
	offsetConv, err := strconv.Atoi(offset)
	if err != nil {
		return err
	}

	selector := selectors.Lookup(view)
	selector.Offset = offsetConv
	return nil
}

func buildLimit(selectors *data.Selectors, requestMetadata *RequestMetadata, limitQuery string) error {
	fieldIt := NewParamIt(limitQuery)
	for fieldIt.Has() {
		param, err := fieldIt.Next()
		if err != nil {
			return err
		}

		aView, ok := paramView(param, requestMetadata)
		if !ok {
			continue
		}

		if !aView.CanUseSelectorLimit() {
			return fmt.Errorf("can't use selector limit on %v view", requestMetadata.MainView.Name)
		}

		if err = updateSelectorLimit(selectors, param.Value, requestMetadata.MainView); err != nil {
			return err
		}

	}

	return nil
}

func updateSelectorLimit(selectors *data.Selectors, limit string, view *data.View) error {
	limitConv, err := strconv.Atoi(limit)
	if err != nil {
		return err
	}

	selector := selectors.Lookup(view)
	selector.Limit = limitConv
	return nil
}

func buildOrderBy(selectors *data.Selectors, requestMetadata *RequestMetadata, orderByQuery string) error {
	fieldIt := NewParamIt(orderByQuery)
	for fieldIt.Has() {
		param, err := fieldIt.Next()
		if err != nil {
			return err
		}

		aView, ok := paramView(param, requestMetadata)
		if !ok {
			continue
		}

		if err = canUseOrderBy(aView, param.Value); err != nil {
			return err
		}

		selector := selectors.Lookup(aView)
		selector.OrderBy = param.Value
	}

	return nil
}

func canUseOrderBy(view *data.View, orderBy string) error {
	if !view.CanUseSelectorOrderBy() {
		return fmt.Errorf("can't use orderBy %v on view %v", orderBy, view.Name)
	}

	_, ok := view.ColumnByName(orderBy)
	if !ok {
		return fmt.Errorf("not found column %v on view %v", orderBy, view.Name)
	}

	return nil
}

func buildCriteria(selectors *data.Selectors, requestMetadata *RequestMetadata, criteriaQuery string) error {
	fieldIt := NewParamIt(criteriaQuery)
	for fieldIt.Has() {
		param, err := fieldIt.Next()
		if err != nil {
			return err
		}

		aView, ok := paramView(param, requestMetadata)
		if !ok {
			continue
		}

		if err = addSelectorCriteria(selectors, aView, param.Value); err != nil {
			return err
		}
	}

	return nil
}

func addSelectorCriteria(selectors *data.Selectors, view *data.View, criteria string) error {
	if !view.CanUseSelectorCriteria() {
		return fmt.Errorf("can't use criteria on view %v", view.Name)
	}

	criteriaSanitized, err := sanitizeCriteria(criteria, view)
	if err != nil {
		return err
	}

	selector := selectors.Lookup(view)
	selector.Criteria = criteriaSanitized
	return nil
}

func sanitizeCriteria(criteria string, view *data.View) (string, error) {
	node, err := sanitize.Parse([]byte(criteria))
	if err != nil {
		return "", err
	}

	sb := strings.Builder{}
	if err = node.Sanitize(&sb, view.IndexedColumns()); err != nil {
		return "", err
	}

	return sb.String(), nil
}
