// Package apidoc defines the documents that refresh prints with -o json and
// -o yaml, and encodes them.
//
// Every document starts with apiVersion and kind, so a consumer can tell
// which contract and which JSON Schema apply. A document never contains
// null: an empty list is [], an empty map is {}, and a key with no data is
// left out.
//
// apidoc imports no other refresh package.
package apidoc

// APIVersion is the version of the document contract.
const APIVersion = "refresh.drod.dev/v2"

// Kind names the type of a document, in PascalCase.
type Kind string

// The document kinds.
const (
	KindVersion     Kind = "Version"
	KindClusterList Kind = "ClusterList"
)

// Kinds returns every kind, in documentation order.
func Kinds() []Kind {
	return []Kind{
		KindVersion,
		KindClusterList,
	}
}

// Document is a value that refresh prints as one JSON or YAML document. It
// must marshal to a JSON object. Kind must not depend on the value.
type Document interface {
	Kind() Kind
}

// Documents returns one zero value of every document type, in the order of
// Kinds. The schema generator and the tests walk it.
func Documents() []Document {
	return []Document{
		Version{},
		ClusterList{},
	}
}

// Command returns the command that prints a document of kind k, without
// the program name, or "" for a kind that is not known.
func (k Kind) Command() string {
	switch k {
	case KindVersion:
		return "version"
	case KindClusterList:
		return "cluster list"
	}
	return ""
}

// Enum is a string type of a document with a closed set of values. The
// schema generator lists the values.
type Enum interface {
	Values() []string
}
