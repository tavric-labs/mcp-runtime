package access

import (
	"encoding/json"
	"time"
)

// Time is a point in time that marshals exactly like Kubernetes metav1.Time:
// RFC 3339 in UTC at second precision, "null" when zero, and local time when
// parsed. It keeps the grant and session wire format unchanged without making
// the access domain depend on Kubernetes API types.
type Time struct {
	time.Time
}

// NewTime wraps t.
func NewTime(t time.Time) Time { return Time{t} }

// DeepCopyInto copies the receiver into out, like metav1.Time.
func (t *Time) DeepCopyInto(out *Time) { *out = *t }

// DeepCopy returns a copy of the receiver, or nil for a nil receiver.
func (t *Time) DeepCopy() *Time {
	if t == nil {
		return nil
	}
	out := new(Time)
	t.DeepCopyInto(out)
	return out
}

// UnmarshalJSON implements json.Unmarshaler with metav1.Time semantics.
func (t *Time) UnmarshalJSON(b []byte) error {
	if len(b) == 4 && string(b) == "null" {
		t.Time = time.Time{}
		return nil
	}
	var str string
	if err := json.Unmarshal(b, &str); err != nil {
		return err
	}
	parsed, err := time.Parse(time.RFC3339, str)
	if err != nil {
		return err
	}
	t.Time = parsed.Local()
	return nil
}

// MarshalJSON implements json.Marshaler with metav1.Time semantics.
func (t Time) MarshalJSON() ([]byte, error) {
	if t.IsZero() {
		return []byte("null"), nil
	}
	buf := make([]byte, 0, len(time.RFC3339)+2)
	buf = append(buf, '"')
	buf = t.UTC().AppendFormat(buf, time.RFC3339)
	buf = append(buf, '"')
	return buf, nil
}

// ConditionStatus is the status value of a Condition.
type ConditionStatus string

// Condition mirrors metav1.Condition field for field, including JSON tags.
type Condition struct {
	Type               string          `json:"type"`
	Status             ConditionStatus `json:"status"`
	ObservedGeneration int64           `json:"observedGeneration,omitempty"`
	LastTransitionTime Time            `json:"lastTransitionTime"`
	Reason             string          `json:"reason"`
	Message            string          `json:"message"`
}
