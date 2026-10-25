package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/deploys"
	"github.com/alexseymer/haetae/internal/deployspec"
	"github.com/alexseymer/haetae/internal/store"
)

func (a *API) registerProjects(mux *http.ServeMux) {
	mux.Handle("POST /api/hosts/{id}/projects", a.authenticate(a.requireAdmin(http.HandlerFunc(a.handleCreateProject))))
	mux.Handle("GET /api/projects", a.authenticate(http.HandlerFunc(a.handleListProjects)))
	mux.Handle("GET /api/projects/{pid}", a.authenticate(http.HandlerFunc(a.handleGetProject)))
	mux.Handle("PATCH /api/projects/{pid}", a.authenticate(a.requireAdmin(http.HandlerFunc(a.handlePatchProject))))
	mux.Handle("DELETE /api/projects/{pid}", a.authenticate(a.requireAdmin(http.HandlerFunc(a.handleDeleteProject))))
	mux.Handle("POST /api/projects/{pid}/deploy", a.authenticate(a.requireAdmin(http.HandlerFunc(a.handleStartDeploy))))
	mux.Handle("GET /api/projects/{pid}/deploys", a.authenticate(http.HandlerFunc(a.handleListDeploys)))
	mux.Handle("GET /api/deploys/{did}", a.authenticate(http.HandlerFunc(a.handleGetDeploy)))
	mux.Handle("GET /api/deploys/{did}/logs", a.authenticate(http.HandlerFunc(a.handleDeployLogs)))
	mux.Handle("POST /api/deploys/{did}/rollback", a.authenticate(a.requireAdmin(http.HandlerFunc(a.handleRollback))))
}

type projectSourceJSON struct {
	Kind          string `json:"kind"`
	GitURL        string `json:"gitUrl"`
	GitRef        string `json:"gitRef"`
	ComposePath   string `json:"composePath"`
	GitToken      string `json:"gitToken"`
	InlineCompose string `json:"inlineCompose"`
}

type createProjectJSON struct {
	Name       string            `json:"name"`
	Source     projectSourceJSON `json:"source"`
	Domain     string            `json:"domain"`
	Service    string            `json:"service"`
	Port       int               `json:"port"`
	AutoDeploy bool              `json:"autoDeploy"`
}

func (a *API) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	st, p := a.scoped(r)
	hostID, ok := parseHostID(w, r.PathValue("id"))
	if !ok {
		return
	}
	if _, ok := a.requireHost(w, r, st, p.OrgID, hostID); !ok {
		return
	}
	var in createProjectJSON
	if !decodeJSON(w, r, &in) {
		return
	}
	pid := uuid.New()
	inProj, err := buildProjectInput(in.Name, in.Source, in.Domain, in.Service, in.Port, in.AutoDeploy)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.Source.GitToken != "" {
		sealed, err := a.sealToken(in.Source.GitToken, pid)
		if err != nil {
			a.log.Error("seal token", "err", err)
			writeError(w, http.StatusServiceUnavailable, "try again")
			return
		}
		inProj.GitTokenSealed = sealed
	}
	proj, err := st.CreateProject(r.Context(), p.OrgID, hostID, pid, inProj)
	if err != nil {
		if errors.Is(err, store.ErrNameTaken) {
			writeError(w, http.StatusConflict, "a project with that name already exists on this host")
			return
		}
		a.log.Error("create project", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	a.refreshWatch(hostID)
	writeJSON(w, http.StatusCreated, map[string]any{"project": projectJSON(proj)})
}

func (a *API) handleListProjects(w http.ResponseWriter, r *http.Request) {
	st, p := a.scoped(r)
	var (
		list []store.Project
		err  error
	)
	if hostStr := r.URL.Query().Get("hostId"); hostStr != "" {
		hostID, ok := parseHostID(w, hostStr)
		if !ok {
			return
		}
		if _, ok := a.requireHost(w, r, st, p.OrgID, hostID); !ok {
			return
		}
		list, err = st.ListProjects(r.Context(), p.OrgID, hostID)
	} else {
		list, err = st.ListAllProjects(r.Context(), p.OrgID)
	}
	if err != nil {
		a.log.Error("list projects", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, proj := range list {
		out = append(out, projectJSON(proj))
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": out})
}

func (a *API) handleGetProject(w http.ResponseWriter, r *http.Request) {
	st, p := a.scoped(r)
	pid, ok := parseUUIDParam(w, r.PathValue("pid"))
	if !ok {
		return
	}
	proj, err := st.GetProject(r.Context(), p.OrgID, pid)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		a.log.Error("get project", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": projectJSON(proj)})
}

func (a *API) handlePatchProject(w http.ResponseWriter, r *http.Request) {
	st, p := a.scoped(r)
	pid, ok := parseUUIDParam(w, r.PathValue("pid"))
	if !ok {
		return
	}
	cur, err := st.GetProject(r.Context(), p.OrgID, pid)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		a.log.Error("get project", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil || len(body) > maxBodyBytes {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var raw map[string]json.RawMessage
	if err := dec.Decode(&raw); err != nil || dec.More() {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	src := projectSourceJSON{
		Kind: cur.SourceKind, GitURL: cur.GitURL, GitRef: cur.GitRef,
		ComposePath: cur.ComposePath, InlineCompose: cur.InlineCompose,
	}
	tokenAction := "" // keep | clear | set
	if srcRaw, ok := raw["source"]; ok {
		var srcMap map[string]json.RawMessage
		if err := json.Unmarshal(srcRaw, &srcMap); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		var partial projectSourceJSON
		if err := json.Unmarshal(srcRaw, &partial); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if partial.Kind != "" {
			src.Kind = partial.Kind
		}
		if _, ok := srcMap["gitUrl"]; ok {
			src.GitURL = partial.GitURL
		}
		if _, ok := srcMap["gitRef"]; ok {
			src.GitRef = partial.GitRef
		}
		if _, ok := srcMap["composePath"]; ok {
			src.ComposePath = partial.ComposePath
		}
		if _, ok := srcMap["inlineCompose"]; ok {
			src.InlineCompose = partial.InlineCompose
		}
		if tokRaw, ok := srcMap["gitToken"]; ok {
			var tok string
			if err := json.Unmarshal(tokRaw, &tok); err != nil {
				writeError(w, http.StatusBadRequest, "invalid JSON")
				return
			}
			if tok == "" {
				tokenAction = "clear"
			} else {
				tokenAction = "set"
				src.GitToken = tok
			}
		}
	}

	domain, service, port, auto := cur.Domain, cur.Service, cur.Port, cur.AutoDeploy
	if v, ok := raw["domain"]; ok {
		if err := json.Unmarshal(v, &domain); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
	}
	if v, ok := raw["service"]; ok {
		if err := json.Unmarshal(v, &service); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
	}
	if v, ok := raw["port"]; ok {
		if err := json.Unmarshal(v, &port); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
	}
	if v, ok := raw["autoDeploy"]; ok {
		if err := json.Unmarshal(v, &auto); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
	}

	upd, err := buildProjectInput(cur.Name, src, domain, service, port, auto)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	switch tokenAction {
	case "clear":
		upd.ClearToken = true
	case "set":
		sealed, err := a.sealToken(src.GitToken, pid)
		if err != nil {
			a.log.Error("seal token", "err", err)
			writeError(w, http.StatusServiceUnavailable, "try again")
			return
		}
		upd.SetToken = true
		upd.GitTokenSealed = sealed
	}

	proj, err := st.UpdateProject(r.Context(), p.OrgID, pid, upd)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		a.log.Error("update project", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	a.refreshWatch(proj.HostID)
	writeJSON(w, http.StatusOK, map[string]any{"project": projectJSON(proj)})
}

func (a *API) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	st, p := a.scoped(r)
	pid, ok := parseUUIDParam(w, r.PathValue("pid"))
	if !ok {
		return
	}
	proj, err := st.GetProject(r.Context(), p.OrgID, pid)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		a.log.Error("get project", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	force := r.URL.Query().Get("force") == "1"
	if a.cfg.Deploys != nil {
		err := a.cfg.Deploys.Remove(r.Context(), proj.HostID, proj.Name)
		if errors.Is(err, deploys.ErrHostOffline) {
			if !force {
				writeError(w, http.StatusConflict, "host is offline; pass force=1 to delete the records anyway")
				return
			}
		} else if err != nil {
			a.log.Error("remove project on agent", "err", err)
			writeError(w, http.StatusServiceUnavailable, "try again")
			return
		}
	} else if !force {
		writeError(w, http.StatusConflict, "host is offline; pass force=1 to delete the records anyway")
		return
	}
	if err := a.st.DeleteProject(r.Context(), p.OrgID, pid); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		a.log.Error("delete project", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	a.refreshWatch(proj.HostID)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleStartDeploy(w http.ResponseWriter, r *http.Request) {
	if a.cfg.Deploys == nil {
		writeError(w, http.StatusServiceUnavailable, "deploys not configured")
		return
	}
	st, p := a.scoped(r)
	pid, ok := parseUUIDParam(w, r.PathValue("pid"))
	if !ok {
		return
	}
	var body struct{}
	if !decodeJSON(w, r, &body) {
		return
	}
	if _, err := st.GetProject(r.Context(), p.OrgID, pid); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		a.log.Error("get project", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	proj, err := a.st.GetProject(r.Context(), p.OrgID, pid)
	if err != nil {
		a.log.Error("get project", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	startedBy := p.UserID
	d, err := a.cfg.Deploys.Start(r.Context(), p.OrgID, proj.HostID, proj, store.TriggerManual, &startedBy, deploys.StartOpts{})
	if errors.Is(err, deploys.ErrBusy) {
		writeError(w, http.StatusConflict, "a deploy is already running for this project")
		return
	}
	if errors.Is(err, deploys.ErrHostOffline) {
		writeError(w, http.StatusServiceUnavailable, "host is not connected")
		return
	}
	if err != nil {
		a.log.Error("start deploy", "err", err)
		writeError(w, http.StatusServiceUnavailable, "deploy failed to start")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"deploy": deployJSON(d, proj.Name)})
}

func (a *API) handleListDeploys(w http.ResponseWriter, r *http.Request) {
	st, p := a.scoped(r)
	pid, ok := parseUUIDParam(w, r.PathValue("pid"))
	if !ok {
		return
	}
	proj, err := st.GetProject(r.Context(), p.OrgID, pid)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		a.log.Error("get project", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	list, err := st.ListDeploys(r.Context(), p.OrgID, pid, 50)
	if err != nil {
		a.log.Error("list deploys", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, d := range list {
		out = append(out, deployJSON(d, proj.Name))
	}
	writeJSON(w, http.StatusOK, map[string]any{"deploys": out})
}

func (a *API) handleGetDeploy(w http.ResponseWriter, r *http.Request) {
	st, p := a.scoped(r)
	did, ok := parseUUIDParam(w, r.PathValue("did"))
	if !ok {
		return
	}
	d, err := st.GetDeploy(r.Context(), p.OrgID, did)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		a.log.Error("get deploy", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	name := ""
	if proj, err := st.GetProject(r.Context(), p.OrgID, d.ProjectID); err == nil {
		name = proj.Name
	}
	writeJSON(w, http.StatusOK, map[string]any{"deploy": deployJSON(d, name)})
}

func (a *API) handleDeployLogs(w http.ResponseWriter, r *http.Request) {
	st, p := a.scoped(r)
	did, ok := parseUUIDParam(w, r.PathValue("did"))
	if !ok {
		return
	}
	d, err := st.GetDeploy(r.Context(), p.OrgID, did)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		a.log.Error("get deploy", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	after := 0
	if s := r.URL.Query().Get("after"); s != "" {
		after, _ = strconv.Atoi(s)
	}
	limit := 500
	if s := r.URL.Query().Get("limit"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			limit = n
		}
	}
	logs, err := st.ListDeployLogs(r.Context(), p.OrgID, did, after, limit)
	if err != nil {
		a.log.Error("list deploy logs", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	out := make([]map[string]any, 0, len(logs))
	for _, l := range logs {
		out = append(out, map[string]any{
			"seq": l.Seq, "stream": l.Stream, "text": l.Text, "withheld": l.Withheld,
		})
	}
	final := d.Status == store.DeployHealthy || d.Status == store.DeployFailed ||
		d.Status == store.DeployRolledBack || d.Status == store.DeployRollbackFailed
	writeJSON(w, http.StatusOK, map[string]any{
		"logs": out, "done": final && len(logs) < limit, "status": d.Status,
	})
}

func (a *API) handleRollback(w http.ResponseWriter, r *http.Request) {
	if a.cfg.Deploys == nil {
		writeError(w, http.StatusServiceUnavailable, "deploys not configured")
		return
	}
	st, p := a.scoped(r)
	did, ok := parseUUIDParam(w, r.PathValue("did"))
	if !ok {
		return
	}
	var body struct{}
	if !decodeJSON(w, r, &body) {
		return
	}
	d, err := st.GetDeploy(r.Context(), p.OrgID, did)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		a.log.Error("get deploy", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	proj, err := a.st.GetProject(r.Context(), p.OrgID, d.ProjectID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	startedBy := p.UserID
	nd, err := a.cfg.Deploys.Rollback(r.Context(), p.OrgID, proj.HostID, proj, did, &startedBy)
	switch {
	case errors.Is(err, deploys.ErrBusy):
		writeError(w, http.StatusConflict, "a deploy is already running for this project")
	case errors.Is(err, deploys.ErrHostOffline):
		writeError(w, http.StatusServiceUnavailable, "host is not connected")
	case errors.Is(err, deploys.ErrNotHealthy):
		writeError(w, http.StatusConflict, "target deploy is not healthy")
	case errors.Is(err, deploys.ErrSameDeploy):
		writeError(w, http.StatusConflict, "cannot roll back to the current deploy")
	case err != nil:
		a.log.Error("rollback", "err", err)
		writeError(w, http.StatusServiceUnavailable, "rollback failed to start")
	default:
		writeJSON(w, http.StatusAccepted, map[string]any{"deploy": deployJSON(nd, proj.Name)})
	}
}

func (a *API) sealToken(plain string, projectID uuid.UUID) ([]byte, error) {
	if a.cfg.Secrets == nil {
		return nil, errors.New("secrets not configured")
	}
	return a.cfg.Secrets.Seal([]byte(plain), projectID.String())
}

func (a *API) refreshWatch(hostID uuid.UUID) {
	if a.cfg.RefreshWatch != nil {
		a.cfg.RefreshWatch(hostID)
	}
}

func parseUUIDParam(w http.ResponseWriter, s string) (uuid.UUID, bool) {
	id, err := uuid.Parse(s)
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return uuid.UUID{}, false
	}
	return id, true
}

func buildProjectInput(name string, src projectSourceJSON, domain, service string, port int, autoDeploy bool) (store.ProjectInput, error) {
	if err := deployspec.ProjectName(name); err != nil {
		return store.ProjectInput{}, err
	}
	if err := deployspec.Domain(domain); err != nil {
		return store.ProjectInput{}, err
	}
	if domain != "" {
		if err := deployspec.Service(service); err != nil {
			return store.ProjectInput{}, err
		}
		if err := deployspec.Port(uint32(port)); err != nil { //nolint:gosec
			return store.ProjectInput{}, err
		}
	} else {
		if service != "" {
			if err := deployspec.Service(service); err != nil {
				return store.ProjectInput{}, err
			}
		}
		if port != 0 {
			if err := deployspec.Port(uint32(port)); err != nil { //nolint:gosec
				return store.ProjectInput{}, err
			}
		}
	}
	if domain != "" && (service == "" || port == 0) {
		return store.ProjectInput{}, errors.New("domain requires service and port")
	}
	in := store.ProjectInput{
		Name: name, Domain: domain, Service: service, Port: port, AutoDeploy: autoDeploy,
	}
	switch src.Kind {
	case "git":
		// HAETAE_ALLOW_LOCAL_GIT=1 lets tests create file:// projects; the agent
		// still needs HAETAE_AGENT_ALLOW_LOCAL_GIT=1 to fetch them.
		allowLocal := os.Getenv("HAETAE_ALLOW_LOCAL_GIT") == "1"
		if err := deployspec.GitURL(src.GitURL, allowLocal); err != nil {
			return store.ProjectInput{}, err
		}
		if err := deployspec.GitRef(src.GitRef); err != nil {
			return store.ProjectInput{}, err
		}
		path, err := deployspec.ComposePath(src.ComposePath)
		if err != nil {
			return store.ProjectInput{}, err
		}
		in.SourceKind = "git"
		in.GitURL = src.GitURL
		in.GitRef = src.GitRef
		in.ComposePath = path
	case "inline":
		if err := deployspec.InlineCompose(src.InlineCompose); err != nil {
			return store.ProjectInput{}, err
		}
		in.SourceKind = "inline"
		in.InlineCompose = src.InlineCompose
	default:
		return store.ProjectInput{}, errors.New(`source.kind must be "git" or "inline"`)
	}
	return in, nil
}

func projectJSON(p store.Project) map[string]any {
	src := map[string]any{"kind": p.SourceKind}
	switch p.SourceKind {
	case "git":
		src["gitUrl"] = p.GitURL
		src["gitRef"] = p.GitRef
		src["composePath"] = p.ComposePath
	case "inline":
		src["inlineCompose"] = p.InlineCompose
	}
	return map[string]any{
		"id": p.ID.String(), "hostId": p.HostID.String(), "name": p.Name,
		"source": src, "domain": p.Domain, "service": p.Service, "port": p.Port,
		"autoDeploy": p.AutoDeploy, "hasToken": p.HasToken,
		"createdAt": rfc3339(p.CreatedAt), "updatedAt": rfc3339(p.UpdatedAt),
	}
}

func deployJSON(d store.Deploy, projectName string) map[string]any {
	var rollbackOf any
	if d.RollbackOf != nil {
		rollbackOf = d.RollbackOf.String()
	}
	return map[string]any{
		"id": d.ID.String(), "projectId": d.ProjectID.String(), "projectName": projectName,
		"status": d.Status, "commit": d.Commit, "trigger": d.Trigger,
		"rollbackOf": rollbackOf,
		"createdAt":  rfc3339(d.CreatedAt),
		"startedAt":  rfc3339Ptr(d.StartedAt),
		"finishedAt": rfc3339Ptr(d.FinishedAt),
		"url":        d.URL, "error": d.Error, "message": d.Message,
	}
}
