package apidoc

import "time"

// AddonHealth is the health of an add-on, from its EKS status.
type AddonHealth string

// The add-on health values.
const (
	// AddonHealthy: the add-on is ACTIVE.
	AddonHealthy AddonHealth = "Healthy"
	// AddonFailed: the add-on is DEGRADED, or a create, an update, or a
	// delete failed.
	AddonFailed AddonHealth = "Failed"
	// AddonInProgress: the add-on is CREATING, UPDATING, or DELETING.
	AddonInProgress AddonHealth = "InProgress"
	// AddonUnknown: any other status.
	AddonUnknown AddonHealth = "Unknown"
)

// Values implements Enum.
func (AddonHealth) Values() []string {
	return []string{string(AddonHealthy), string(AddonFailed), string(AddonInProgress), string(AddonUnknown)}
}

// Addon is one installed EKS add-on. A key is left out when its data was
// not read: an add-on that could not be described has its name and
// incomplete.
type Addon struct {
	Name    string `json:"name"`
	Version string `json:"version,omitzero"`
	// Status is the EKS status, such as ACTIVE or DEGRADED.
	Status string `json:"status,omitzero"`
	// Health is the status as one of a closed set of values.
	Health                AddonHealth `json:"health,omitzero"`
	ARN                   string      `json:"arn,omitzero"`
	ServiceAccountRoleARN string      `json:"serviceAccountRoleArn,omitzero"`
	// Configuration is the configuration values of the add-on, as the
	// JSON or YAML text that EKS returns.
	Configuration string `json:"configuration,omitzero"`
	// Issues are the health issues that EKS reports for the add-on.
	Issues     []Issue   `json:"issues,omitzero"`
	CreatedAt  time.Time `json:"createdAt,omitzero"`
	ModifiedAt time.Time `json:"modifiedAt,omitzero"`
	// Incomplete is true when the add-on could not be read. Its failure is
	// in the failures list of the document.
	Incomplete bool `json:"incomplete,omitzero"`
}

// AddonList is the document of "refresh addon list".
type AddonList struct {
	Cluster  string   `json:"cluster"`
	Region   string   `json:"region"`
	Addons   []Addon  `json:"addons"`
	Failures Failures `json:"failures"`
}

// Kind implements Document.
func (AddonList) Kind() Kind { return KindAddonList }

// AddonDescription is the document of "refresh addon describe".
type AddonDescription struct {
	Cluster  string   `json:"cluster"`
	Region   string   `json:"region"`
	Addon    Addon    `json:"addon"`
	Failures Failures `json:"failures"`
}

// Kind implements Document.
func (AddonDescription) Kind() Kind { return KindAddonDescription }
