package project

import (
	"net/url"
	"strings"
)

// ScrollKey scopes authored scroll names by their component-instance breadcrumb.
func ScrollKey(node *Node) string {
	if node == nil {
		return ""
	}
	name := node.Name
	if name == "" {
		return node.Handle
	}
	parts := make([]string, 0, len(node.Breadcrumb)+1)
	for _, segment := range node.Breadcrumb {
		parts = append(parts, url.PathEscape(segment))
	}
	parts = append(parts, url.PathEscape(name))
	return strings.Join(parts, "/")
}
