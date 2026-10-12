package apidoc

// Version is the document of "refresh version".
type Version struct {
	Version   string `json:"version"`
	Commit    string `json:"commit,omitempty"`
	BuildDate string `json:"buildDate,omitempty"`
}

// Kind implements Document.
func (Version) Kind() Kind { return KindVersion }
