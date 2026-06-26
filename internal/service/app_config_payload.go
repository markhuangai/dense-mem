package service

import (
	"sort"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func generalSettingsPayload(settings *domain.GeneralConfigSettings) map[string]any {
	if settings == nil {
		return nil
	}
	items := make([]map[string]string, 0, len(settings.Items))
	for _, item := range settings.Items {
		items = append(items, map[string]string{
			"key":   item.Key,
			"value": item.Value,
		})
	}
	sortPayloadItems(items)
	return map[string]any{
		"update_time": settings.UpdateTime,
		"items":       items,
	}
}

func ssoSettingsPayload(settings *domain.SSOConfigSettings) map[string]any {
	if settings == nil {
		return nil
	}
	items := make([]map[string]string, 0, len(settings.Items))
	for _, item := range settings.Items {
		items = append(items, map[string]string{
			"key":   item.Key,
			"value": item.Value,
		})
	}
	sortPayloadItems(items)
	return map[string]any{
		"update_time": settings.UpdateTime,
		"items":       items,
	}
}

func dreamingSettingsPayload(settings *domain.DreamingConfigSettings) map[string]any {
	if settings == nil {
		return nil
	}
	items := make([]map[string]string, 0, len(settings.Items))
	for _, item := range settings.Items {
		items = append(items, map[string]string{
			"key":   item.Key,
			"value": item.Value,
		})
	}
	sortPayloadItems(items)
	return map[string]any{
		"update_time": settings.UpdateTime,
		"items":       items,
	}
}

func communityDetectionSettingsPayload(settings *domain.CommunityDetectionConfigSettings) map[string]any {
	if settings == nil {
		return nil
	}
	items := make([]map[string]string, 0, len(settings.Items))
	for _, item := range settings.Items {
		items = append(items, map[string]string{
			"key":   item.Key,
			"value": item.Value,
		})
	}
	sortPayloadItems(items)
	return map[string]any{
		"update_time": settings.UpdateTime,
		"items":       items,
	}
}

func operationLogSettingsPayload(settings *domain.OperationLogConfigSettings) map[string]any {
	if settings == nil {
		return nil
	}
	items := make([]map[string]string, 0, len(settings.Items))
	for _, item := range settings.Items {
		items = append(items, map[string]string{
			"key":   item.Key,
			"value": item.Value,
		})
	}
	sortPayloadItems(items)
	return map[string]any{
		"update_time": settings.UpdateTime,
		"items":       items,
	}
}

func recallFeedbackSettingsPayload(settings *domain.RecallFeedbackConfigSettings) map[string]any {
	if settings == nil {
		return nil
	}
	items := make([]map[string]string, 0, len(settings.Items))
	for _, item := range settings.Items {
		items = append(items, map[string]string{
			"key":   item.Key,
			"value": item.Value,
		})
	}
	sortPayloadItems(items)
	return map[string]any{
		"update_time": settings.UpdateTime,
		"items":       items,
	}
}

func evaluationSettingsPayload(settings *domain.EvaluationConfigSettings) map[string]any {
	if settings == nil {
		return nil
	}
	items := make([]map[string]string, 0, len(settings.Items))
	for _, item := range settings.Items {
		items = append(items, map[string]string{
			"key":   item.Key,
			"value": item.Value,
		})
	}
	sortPayloadItems(items)
	return map[string]any{
		"update_time": settings.UpdateTime,
		"items":       items,
	}
}

func sortPayloadItems(items []map[string]string) {
	sort.Slice(items, func(i, j int) bool { return items[i]["key"] < items[j]["key"] })
}
