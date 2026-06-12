package httpapi

import (
	"net/http"
	"os"
	"time"

	"chatgpt2api/internal/service"
	"chatgpt2api/internal/util"
	"chatgpt2api/internal/version"
)

func (a *App) handleHealth(w http.ResponseWriter, _ *http.Request) {
	util.WriteJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"version": version.Get(),
	})
}

func (a *App) handleAdminSystem(w http.ResponseWriter, r *http.Request) {
	identity, ok := a.requireIdentity(w, r, "")
	if !ok {
		return
	}
	if identity.Role != service.AuthRoleAdmin {
		util.WriteError(w, http.StatusForbidden, "admin permission required")
		return
	}

	base := "/api/admin/system"
	switch r.URL.Path {
	case base + "/version":
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		info, _ := a.update.CheckUpdate(r.Context(), false)
		util.WriteJSON(w, http.StatusOK, map[string]any{
			"version":       version.Get(),
			"version_info":  version.GetInfo(),
			"update_status": info,
		})
	case base + "/check-updates":
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		info, err := a.update.CheckUpdate(r.Context(), r.URL.Query().Get("force") == "true")
		if err != nil {
			util.WriteError(w, http.StatusBadGateway, err.Error())
			return
		}
		util.WriteJSON(w, http.StatusOK, info)
	case base + "/update":
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if err := a.update.PerformUpdate(r.Context()); err != nil {
			util.WriteError(w, http.StatusBadGateway, err.Error())
			return
		}
		util.WriteJSON(w, http.StatusOK, map[string]any{
			"message":      "Update completed. Please restart the service.",
			"need_restart": true,
		})
	case base + "/rollback":
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if err := a.update.Rollback(); err != nil {
			util.WriteError(w, http.StatusBadGateway, err.Error())
			return
		}
		util.WriteJSON(w, http.StatusOK, map[string]any{
			"message":      "Rollback completed. Please restart the service.",
			"need_restart": true,
		})
	case base + "/restart":
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		go func() {
			time.Sleep(500 * time.Millisecond)
			os.Exit(0)
		}()
		util.WriteJSON(w, http.StatusOK, map[string]any{"message": "Service restart initiated"})
	default:
		http.NotFound(w, r)
	}
}
