package domain

// Built-in RBAC roles.
const (
	RoleAdmin    = "admin"
	RoleOperator = "operator"
	RoleViewer   = "viewer"
)

// Rule evaluation modes.
const (
	RuleModeAnomalyDetect  = "anomaly_detect"
	RuleModeCountThreshold = "count_threshold"
	RuleModeAbsenceDetect  = "absence_detect"
	RuleModeSOPSequence    = "sop_sequence"
)

// ROI geometry hit modes.
const (
	HitPointBottomCenter  = "bottom_center"
	HitPointCenter        = "center"
	HitPointBBoxIntersect = "bbox_intersect"
)

// ROI types.
const (
	ROITypePolygon   = "polygon"
	ROITypeFullFrame = "full_frame"
)

// Alarm severity levels.
const (
	SeverityInfo     = "info"
	SeverityWarning  = "warning"
	SeverityCritical = "critical"
)

// Alarm handling statuses.
const (
	HandleStatusUnhandled     = "unhandled"
	HandleStatusConfirmed     = "confirmed"
	HandleStatusFalsePositive = "false_positive"
	HandleStatusIgnored       = "ignored"
)

// False positive reason codes.
const (
	FalsePositiveLighting      = "lighting"
	FalsePositiveOcclusion     = "occlusion"
	FalsePositiveSimilarAction = "similar_action"
	FalsePositiveModelDrift    = "model_drift"
	FalsePositiveOther         = "other"
)

// Spatial hierarchy zone types.
const (
	ZoneTypeFactory     = "factory"
	ZoneTypeWorkshop    = "workshop"
	ZoneTypeLine        = "line"
	ZoneTypeWorkstation = "workstation"
)

// SOP cycle outcomes.
const (
	SOPOutcomeCompliant = "compliant"
	SOPOutcomeViolated  = "violated"
)

// Alarm video evidence statuses.
const (
	VideoStatusNone      = "none"
	VideoStatusRecording = "recording"
	VideoStatusReady     = "ready"
	VideoStatusFailed    = "failed"
)

// Inference task states.
const (
	TaskDesiredRunning = "running"
	TaskDesiredStopped = "stopped"

	TaskActualPending      = "pending"
	TaskActualRunning      = "running"
	TaskActualReconnecting = "reconnecting"
	TaskActualFailed       = "failed"
	TaskActualStopped      = "stopped"
)

// Agent node statuses.
const (
	NodeStatusOnline   = "online"
	NodeStatusOffline  = "offline"
	NodeStatusDraining = "draining"
)
