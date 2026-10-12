package plugins

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"vartio/internal/common"
)

// configEntry is one plugin's settings for one probe.
type configEntry struct {
	Name   string            `json:"name"`
	Agent  string            `json:"agent"`
	Values map[string]string `json:"values"`
}

type settingField struct {
	Key     string `json:"key"`
	Title   string `json:"title"`
	Help    string `json:"help"`
	Kind    string `json:"kind"`
	Default string `json:"default,omitempty"`
}

func settingsFor(name string) []settingField {
	switch name {
	case "cdp":
		return []settingField{{
			Key:   "browsers",
			Title: "额外浏览器",
			Kind:  "lines",
			Help:  "每行一个，写在这台探针上的可执行文件。可以用「名称|路径」，例如 预策浏览器|/Applications/预策浏览器.app/Contents/MacOS/预策浏览器",
		}}
	case "bitbrowser":
		return []settingField{{
			Key:     "port",
			Title:   "本地 API 端口",
			Kind:    "port",
			Default: "54345",
			Help:    "这台探针上比特浏览器本地 API 的端口。不同机器可以不同，默认 54345。",
		}}
	default:
		return nil
	}
}

func portSetting(name string) bool {
	for _, f := range settingsFor(name) {
		if f.Kind == "port" {
			return true
		}
	}
	return false
}

func cleanConfigs(in []configEntry) []configEntry {
	out := make([]configEntry, 0, len(in))
	seen := map[string]bool{}
	for _, e := range in {
		e.Name = strings.TrimSpace(e.Name)
		e.Agent = strings.TrimSpace(e.Agent)
		if e.Agent == "" || !pluginName.MatchString(e.Name) || seen[e.Name+"\n"+e.Agent] {
			continue
		}
		vals, err := normalizeConfig(e.Name, e.Values)
		if err != nil || len(vals) == 0 {
			continue
		}
		e.Values = vals
		seen[e.Name+"\n"+e.Agent] = true
		out = append(out, e)
	}
	return out
}

func normalizeConfig(name string, in map[string]string) (map[string]string, error) {
	fields := settingsFor(name)
	if len(fields) == 0 {
		return nil, common.ErrBad("这个插件没有可配置项")
	}
	allowed := map[string]settingField{}
	for _, f := range fields {
		allowed[f.Key] = f
	}
	out := map[string]string{}
	for k, v := range in {
		f, ok := allowed[k]
		if !ok {
			return nil, common.ErrBad("不认识的配置 %s", k)
		}
		v = strings.TrimSpace(v)
		if v == "" || v == f.Default {
			continue
		}
		switch f.Kind {
		case "port":
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > 65535 {
				return nil, common.ErrBad("端口需要是 1 到 65535")
			}
			out[k] = strconv.Itoa(n)
		case "lines":
			cleaned, err := cleanLines(v)
			if err != nil {
				return nil, err
			}
			if cleaned != "" {
				out[k] = cleaned
			}
		default:
			return nil, common.ErrBad("不认识的配置 %s", k)
		}
	}
	return out, nil
}

func cleanLines(raw string) (string, error) {
	var lines []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, path, _ := strings.Cut(line, "|")
		name, path = strings.TrimSpace(name), strings.TrimSpace(path)
		if path == "" {
			path = name
			name = ""
		}
		if utf8.RuneCountInString(name) > 40 || len(path) > 512 || strings.ContainsAny(path, "\r\n") {
			return "", common.ErrBad("浏览器路径不合法")
		}
		if name == "" {
			lines = append(lines, path)
		} else {
			lines = append(lines, name+"|"+path)
		}
		if len(lines) > 20 {
			return "", common.ErrBad("最多 20 条额外浏览器")
		}
	}
	return strings.Join(lines, "\n"), nil
}

func (r *Registry) configValues(name, agent string) map[string]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]string{}
	for _, c := range r.configs {
		if c.Name == name && c.Agent == agent {
			for k, v := range c.Values {
				out[k] = v
			}
		}
	}
	return out
}

func (r *Registry) handleGetConfig(w http.ResponseWriter, req *http.Request) {
	name := req.PathValue("name")
	if _, ok := r.item(name, nil); !ok {
		WriteError(w, errNotFound(name))
		return
	}
	fields := settingsFor(name)
	if fields == nil {
		fields = []settingField{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"fields": fields,
		"values": r.configsFor(name),
	})
}

func (r *Registry) configsFor(name string) []configEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []configEntry{}
	for _, c := range r.configs {
		if c.Name == name {
			out = append(out, c)
		}
	}
	return out
}

func (r *Registry) handlePutConfig(w http.ResponseWriter, req *http.Request) {
	name := req.PathValue("name")
	if _, ok := r.item(name, nil); !ok {
		WriteError(w, errNotFound(name))
		return
	}
	var body struct {
		Agent  string            `json:"agent"`
		Values map[string]string `json:"values"`
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		WriteError(w, common.ErrBad("invalid json"))
		return
	}
	body.Agent = strings.TrimSpace(body.Agent)
	if body.Agent == "" {
		WriteError(w, common.ErrBad("需要指定探针"))
		return
	}
	vals, err := normalizeConfig(name, body.Values)
	if err != nil {
		WriteError(w, err)
		return
	}
	r.mu.Lock()
	next := make([]configEntry, 0, len(r.configs))
	for _, c := range r.configs {
		if c.Name == name && c.Agent == body.Agent {
			continue
		}
		next = append(next, c)
	}
	if len(vals) > 0 {
		next = append(next, configEntry{Name: name, Agent: body.Agent, Values: vals})
	}
	r.configs = next
	err = r.saveLocked()
	r.mu.Unlock()
	if err != nil {
		WriteError(w, err)
		return
	}
	r.handleGetConfig(w, req)
}
