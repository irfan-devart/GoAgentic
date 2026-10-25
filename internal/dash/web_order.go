package dash

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The principal's own order for the sidebar: workspaces, and each
// workspace's agents, dragged into place. It is a view preference, kept in
// the user's config folder beside nothing else of the agents', so the same
// order shows in every browser.

type sidebarOrder struct {
	Workspaces []string            `json:"workspaces,omitempty"`
	Agents     map[string][]string `json:"agents,omitempty"`
}

// maxOrder bounds the names one reorder may send.
const maxOrder = 500

func (s *webServer) orderPath() string {
	if s.src.OrderFile != "" {
		return s.src.OrderFile
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "goagentic", "dash-order.json")
}

func (s *webServer) loadOrder() sidebarOrder {
	var o sidebarOrder
	if path := s.orderPath(); path != "" {
		if data, err := os.ReadFile(path); err == nil {
			_ = json.Unmarshal(data, &o)
		}
	}
	return o
}

func (s *webServer) saveOrder(o sidebarOrder) error {
	path := s.orderPath()
	if path == "" {
		return os.ErrNotExist
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(o, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// inOrder sorts items by their place in order; names it does not list keep
// their own order, after the listed ones.
func inOrder[T any](items []T, name func(T) string, order []string) []T {
	if len(order) == 0 {
		return items
	}
	at := map[string]int{}
	for i, n := range order {
		at[n] = i
	}
	out := append([]T(nil), items...)
	sort.SliceStable(out, func(i, j int) bool {
		a, aok := at[name(out[i])]
		b, bok := at[name(out[j])]
		switch {
		case aok && bok:
			return a < b
		default:
			return aok && !bok
		}
	})
	return out
}

// reorder saves a new sidebar order: kind=workspaces, or kind=agents with
// ws naming the workspace, and the names in their new order.
func (s *webServer) reorder(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var names []string
	for _, n := range r.PostForm["name"] {
		if n = strings.TrimSpace(n); n != "" {
			names = append(names, n)
		}
	}
	if len(names) == 0 || len(names) > maxOrder {
		http.Error(w, "no order to save", http.StatusBadRequest)
		return
	}
	s.orderMu.Lock()
	defer s.orderMu.Unlock()
	o := s.loadOrder()
	switch kind, ws := r.FormValue("kind"), r.FormValue("ws"); {
	case kind == "workspaces":
		o.Workspaces = names
	case kind == "agents" && ws != "":
		if o.Agents == nil {
			o.Agents = map[string][]string{}
		}
		o.Agents[ws] = names
	default:
		http.Error(w, "kind must be workspaces, or agents with ws", http.StatusBadRequest)
		return
	}
	if err := s.saveOrder(o); err != nil {
		http.Error(w, "could not save the order: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
