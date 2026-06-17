package api

import (
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/browserwing/browserwing/models"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type pageRecordingDetailResponse struct {
	Page      pageRecordingDetailPage `json:"page"`
	Recording pageRecordingDetail     `json:"recording"`
}

type pageRecordingDetailPage struct {
	ID          uint   `json:"id"`
	Name        string `json:"name"`
	Path        string `json:"path"`
	Description string `json:"description"`
}

type pageRecordingDetail struct {
	ID                uint                     `json:"id"`
	PageID            uint                     `json:"page_id"`
	Name              string                   `json:"name"`
	CreatedAt         any                      `json:"created_at"`
	UpdatedAt         any                      `json:"updated_at"`
	ActionTraceJSON   any                      `json:"action_trace_json"`
	DOMSnapshotJSON   any                      `json:"dom_snapshot_json"`
	RecordingMetaJSON any                      `json:"recording_meta_json"`
	Diagnostics       pageRecordingDiagnostics `json:"diagnostics"`
}

type pageRecordingDiagnostics struct {
	ActionCount            int                       `json:"action_count"`
	SnapshotElementCount   int                       `json:"snapshot_element_count"`
	QualityCodes           []string                  `json:"quality_codes"`
	ParseErrors            []pageRecordingParseError `json:"parse_errors"`
	SensitiveFieldsRemoved []string                  `json:"sensitive_fields_removed"`
}

type pageRecordingParseError struct {
	Field string `json:"field"`
	Code  string `json:"code"`
}

// GetLatestPageRecording returns the current saved main-flow recording for a page.
func (h *ProjectHandlers) GetLatestPageRecording(c *gin.Context) {
	projectID, versionID, pageID, ok := parseProjectVersionPageIDs(c)
	if !ok {
		return
	}
	_, page, err := loadGenerationPageContext(h.gormDB(), projectID, versionID, pageID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Project, version, or page not found", "code": "page_not_found"})
		return
	}

	var script models.PageScript
	if err := h.gormDB().Where("page_id = ?", page.ID).Order("created_at desc, id desc").First(&script).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "page_recording_not_found", "code": "page_recording_not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, pageRecordingDetailResponse{
		Page: pageRecordingDetailPage{
			ID:          page.ID,
			Name:        page.Name,
			Path:        page.Path,
			Description: page.Description,
		},
		Recording: buildPageRecordingDetail(script),
	})
}

func buildPageRecordingDetail(script models.PageScript) pageRecordingDetail {
	removed := map[string]struct{}{}
	qualityCodes := map[string]struct{}{}
	parseErrors := []pageRecordingParseError{}

	actions, actionTraceJSON, err := parsePageRecordingActionTrace(script.ActionTrace, removed)
	if err != nil {
		parseErrors = append(parseErrors, pageRecordingParseError{Field: "action_trace", Code: "recording_action_trace_invalid"})
	}

	snapshot, snapshotJSON, err := parsePageRecordingObject(script.DOMSnapshot, removed)
	if err != nil {
		parseErrors = append(parseErrors, pageRecordingParseError{Field: "dom_snapshot", Code: "recording_dom_snapshot_invalid"})
		qualityCodes["recording_snapshot_unusable"] = struct{}{}
	}

	meta, metaJSON, err := parsePageRecordingMeta(script.RecordingMetaJSON, removed)
	if err != nil {
		parseErrors = append(parseErrors, pageRecordingParseError{Field: "recording_meta", Code: "recording_meta_invalid"})
		qualityCodes["recording_meta_invalid"] = struct{}{}
	}

	snapshotElementCount := countRecordingSnapshotElements(snapshot)
	if snapshot == nil || snapshotElementCount == 0 {
		qualityCodes["recording_snapshot_unusable"] = struct{}{}
	}
	addRecordingActionDiagnostics(actions, meta, qualityCodes)

	return pageRecordingDetail{
		ID:                script.ID,
		PageID:            script.PageID,
		Name:              script.Name,
		CreatedAt:         script.CreatedAt,
		UpdatedAt:         script.UpdatedAt,
		ActionTraceJSON:   actionTraceJSON,
		DOMSnapshotJSON:   snapshotJSON,
		RecordingMetaJSON: metaJSON,
		Diagnostics: pageRecordingDiagnostics{
			ActionCount:            len(actions),
			SnapshotElementCount:   snapshotElementCount,
			QualityCodes:           sortedRecordingKeys(qualityCodes),
			ParseErrors:            parseErrors,
			SensitiveFieldsRemoved: sortedRecordingKeys(removed),
		},
	}
}

func parsePageRecordingActionTrace(raw string, removed map[string]struct{}) ([]map[string]any, any, error) {
	parsed, err := parseRequiredJSON(raw, "主流程录制 JSON 非法")
	if err != nil {
		return nil, nil, err
	}
	sanitized := sanitizePageRecordingDisplayValue(parsed, removed)
	actions := actionTraceFromParsed(sanitized)
	if actions == nil {
		actions = []map[string]any{}
	}
	return actions, actions, nil
}

func parsePageRecordingObject(raw string, removed map[string]struct{}) (map[string]any, any, error) {
	parsed, err := parseRequiredJSON(raw, "页面快照 JSON 非法")
	if err != nil {
		return nil, nil, err
	}
	sanitized, _ := sanitizePageRecordingDisplayValue(parsed, removed).(map[string]any)
	if sanitized == nil {
		return nil, nil, errors.New("页面快照 JSON 非法")
	}
	return sanitized, sanitized, nil
}

func parsePageRecordingMeta(raw string, removed map[string]struct{}) (p45RecordingMeta, any, error) {
	meta, _, err := parseRecordingMetaJSON(raw)
	if err != nil {
		return p45RecordingMeta{}, nil, err
	}
	parsed, err := parseRequiredJSON(raw, "recording_meta JSON is invalid")
	if err != nil {
		return p45RecordingMeta{}, nil, err
	}
	return meta, sanitizePageRecordingDisplayValue(parsed, removed), nil
}

func sanitizePageRecordingDisplayValue(value any, removed map[string]struct{}) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			if p475ForbiddenKey(key) {
				removed[key] = struct{}{}
				continue
			}
			sanitized := sanitizePageRecordingDisplayValue(item, removed)
			if sanitized != nil {
				out[key] = sanitized
			}
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			sanitized := sanitizePageRecordingDisplayValue(item, removed)
			if sanitized != nil {
				out = append(out, sanitized)
			}
		}
		return out
	case string:
		if p475ForbiddenString(typed) {
			removed["windows_absolute_path"] = struct{}{}
			return nil
		}
		return typed
	default:
		return value
	}
}

func countRecordingSnapshotElements(snapshot map[string]any) int {
	if snapshot == nil {
		return 0
	}
	items, ok := snapshot["elements"].([]any)
	if !ok {
		return 0
	}
	return len(items)
}

func addRecordingActionDiagnostics(actions []map[string]any, meta p45RecordingMeta, qualityCodes map[string]struct{}) {
	for _, action := range actions {
		actionType := strings.TrimSpace(stringFromAny(action["type"]))
		if actionType == "" {
			actionType = strings.TrimSpace(stringFromAny(action["action"]))
		}
		switch actionType {
		case "navigate":
			if strings.TrimSpace(stringFromAny(action["url"])) == "" && strings.TrimSpace(meta.TargetURL) == "" {
				qualityCodes["recording_navigation_missing_url"] = struct{}{}
			}
		case "fill", "input", "select", "expect_text":
			if !hasPageRecordingActionTarget(action) {
				qualityCodes["recording_action_missing_target"] = struct{}{}
				continue
			}
			if strings.TrimSpace(stringFromAny(action["value"])) == "" {
				qualityCodes["recording_action_missing_value"] = struct{}{}
			}
		case "click", "expect_visible", "wait":
			if !hasPageRecordingActionTarget(action) {
				qualityCodes["recording_action_missing_target"] = struct{}{}
			}
		}
	}
}

func hasPageRecordingActionTarget(action map[string]any) bool {
	if strings.TrimSpace(stringFromAny(action["ref_id"])) != "" ||
		strings.TrimSpace(stringFromAny(action["recorded_selector"])) != "" ||
		strings.TrimSpace(stringFromAny(action["selector"])) != "" ||
		strings.TrimSpace(stringFromAny(action["xpath"])) != "" ||
		strings.TrimSpace(stringFromAny(action["text"])) != "" {
		return true
	}
	target, ok := action["target"].(map[string]any)
	if !ok || len(target) == 0 {
		return false
	}
	if strings.TrimSpace(stringFromAny(target["ref_id"])) != "" ||
		strings.TrimSpace(stringFromAny(target["recorded_selector"])) != "" ||
		strings.TrimSpace(stringFromAny(target["selector"])) != "" ||
		strings.TrimSpace(stringFromAny(target["css"])) != "" ||
		strings.TrimSpace(stringFromAny(target["xpath"])) != "" ||
		strings.TrimSpace(stringFromAny(target["text"])) != "" ||
		strings.TrimSpace(stringFromAny(target["label"])) != "" ||
		strings.TrimSpace(stringFromAny(target["placeholder"])) != "" {
		return true
	}
	return strings.TrimSpace(stringFromAny(target["role"])) != "" && strings.TrimSpace(stringFromAny(target["text"])) != ""
}

func sortedRecordingKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
