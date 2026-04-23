package alert

import "time"

// Severity classifies how urgent an alert is.
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// Alert is the common output type for all detectors.
type Alert struct {
	Detector   string    `json:"detector"`    // detector that fired, e.g. "volume_spike"
	Symbol     string    `json:"symbol"`      // asset pair the alert is for
	Exchange   string    `json:"exchange"`    // exchange the signal originated from, empty = cross-exchange
	Severity   Severity  `json:"severity"`
	Message    string    `json:"message"`     // human-readable description
	Value      float64   `json:"value"`       // the measured quantity that triggered the alert
	Threshold  float64   `json:"threshold"`   // the threshold it crossed
	Timestamp  time.Time `json:"timestamp"`   // when the alert was generated (UTC)
}
