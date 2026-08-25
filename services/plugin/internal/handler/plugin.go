package handler

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/openfield/server/pkg/database"
)

const (
	maxBundleBytes = 5 << 20 // 5 MB plugin bundle cap
	maxEntryBytes  = 512 << 10
)

var (
	idPattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{2,63}$`)
	versionPattern = regexp.MustCompile(`^\d+\.\d+\.\d+(?:[-+][A-Za-z0-9.-]+)?$`)
	permPattern    = regexp.MustCompile(`^[a-z0-9]+(?:\.[a-z0-9_]+){0,3}$`)
)

// manifest is the manifest.json every plugin bundle must carry at its root.
type manifest struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Version       string   `json:"version"`
	Author        string   `json:"author"`
	Description   string   `json:"description"`
	Entry         string   `json:"entry"`
	Permissions   []string `json:"permissions"`
	MinAppVersion string   `json:"min_app_version"`
}

// PluginHandler serves the plugin store catalog and admin management API.
type PluginHandler struct {
	dataDir string
}

// NewPluginHandler builds a handler storing bundles under dataDir.
func NewPluginHandler(dataDir string) *PluginHandler {
	return &PluginHandler{dataDir: dataDir}
}

func db() *sql.DB { return database.DB }

// Plugin is one row of the store catalog.
type Plugin struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Version       string   `json:"version"`
	Author        string   `json:"author"`
	Description   string   `json:"description"`
	Permissions   []string `json:"permissions"`
	MinAppVersion string   `json:"min_app_version"`
	Verified      bool     `json:"verified"`
	Published     bool     `json:"published"`
	Downloads     int64    `json:"downloads"`
	FileSize      int64    `json:"file_size"`
	SHA256        string   `json:"sha256"`
	CreatedAt     string   `json:"created_at"`
	UpdatedAt     string   `json:"updated_at"`
}

const pluginColumns = `id, name, version, author, description, permissions,
	min_app_version, verified, published, downloads, file_size, sha256, created_at, updated_at`

func scanPlugin(row interface{ Scan(...any) error }) (*Plugin, error) {
	var p Plugin
	var perms string
	if err := row.Scan(&p.ID, &p.Name, &p.Version, &p.Author, &p.Description, &perms,
		&p.MinAppVersion, &p.Verified, &p.Published, &p.Downloads, &p.FileSize, &p.SHA256,
		&p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	p.Permissions = parsePerms(perms)
	return &p, nil
}

func parsePerms(raw string) []string {
	out := []string{}
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &out)
	}
	if out == nil {
		out = []string{}
	}
	return out
}

// List GET /api/v1/plugins — published plugins only.
func (h *PluginHandler) List(c *gin.Context) {
	rows, err := db().Query(
		`SELECT `+pluginColumns+` FROM plugins WHERE published = TRUE ORDER BY updated_at DESC`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list plugins"})
		return
	}
	defer rows.Close()

	plugins := []*Plugin{}
	for rows.Next() {
		p, err := scanPlugin(rows)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read plugins"})
			return
		}
		plugins = append(plugins, p)
	}
	c.JSON(http.StatusOK, gin.H{"plugins": plugins})
}

// Get GET /api/v1/plugins/:id — one published plugin.
func (h *PluginHandler) Get(c *gin.Context) {
	row := db().QueryRow(
		`SELECT `+pluginColumns+` FROM plugins WHERE id = $1 AND published = TRUE`,
		c.Param("id"))
	p, err := scanPlugin(row)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "plugin not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load plugin"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"plugin": p})
}

// Download GET /api/v1/plugins/:id/download — stream the stored bundle.
func (h *PluginHandler) Download(c *gin.Context) {
	row := db().QueryRow(
		`SELECT file_path, file_size, name, version FROM plugins WHERE id = $1 AND published = TRUE`,
		c.Param("id"))
	var path, name, version string
	var size int64
	if err := row.Scan(&path, &size, &name, &version); err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "plugin not found"})
		return
	} else if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load plugin"})
		return
	}

	f, err := os.Open(path)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "bundle missing on server"})
		return
	}
	defer f.Close()

	_, _ = db().Exec(`UPDATE plugins SET downloads = downloads + 1 WHERE id = $1`, c.Param("id"))

	filename := fmt.Sprintf("%s-%s.zip", c.Param("id"), version)
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	c.DataFromReader(http.StatusOK, size, "application/zip", f, nil)
}

// Upload POST /api/v1/plugins/admin/upload — multipart form:
//
//	file: <plugin>.zip containing manifest.json at its root
//	publish: optional "true" to publish immediately
func (h *PluginHandler) Upload(c *gin.Context) {
	fh, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing file field"})
		return
	}
	if fh.Size > maxBundleBytes {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("bundle too large (max %d bytes)", maxBundleBytes)})
		return
	}
	src, err := fh.Open()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cannot read upload"})
		return
	}
	defer src.Close()
	buf, err := io.ReadAll(io.LimitReader(src, maxBundleBytes+1))
	if err != nil || int64(len(buf)) > maxBundleBytes {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cannot read upload"})
		return
	}

	m, entryCode, err := inspectBundle(buf)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	sum := sha256.Sum256(buf)
	shaHex := hex.EncodeToString(sum[:])

	if err := os.MkdirAll(h.dataDir, 0o755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "storage unavailable"})
		return
	}
	dstPath := filepath.Join(h.dataDir, m.ID+"-"+m.Version+".zip")
	if err := os.WriteFile(dstPath, buf, 0o644); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save bundle"})
		return
	}

	perms, _ := json.Marshal(m.Permissions)
	publish := c.PostForm("publish") == "true"

	_, err = db().Exec(`
		INSERT INTO plugins (id, name, version, author, description, permissions,
			min_app_version, entry, file_path, file_size, sha256, verified, published)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,TRUE,$12)
		ON CONFLICT (id) DO UPDATE SET
			name = EXCLUDED.name, version = EXCLUDED.version, author = EXCLUDED.author,
			description = EXCLUDED.description, permissions = EXCLUDED.permissions,
			min_app_version = EXCLUDED.min_app_version, entry = EXCLUDED.entry,
			file_path = EXCLUDED.file_path, file_size = EXCLUDED.file_size,
			sha256 = EXCLUDED.sha256, verified = TRUE, published = EXCLUDED.published,
			updated_at = NOW()`,
		m.ID, m.Name, m.Version, m.Author, m.Description, string(perms),
		m.MinAppVersion, m.Entry, dstPath, len(buf), shaHex, publish)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save plugin"})
		return
	}

	_ = entryCode // validated above; kept for future server-side linting
	row := db().QueryRow(`SELECT `+pluginColumns+` FROM plugins WHERE id = $1`, m.ID)
	p, err := scanPlugin(row)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "uploaded"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "uploaded", "plugin": p})
}

// Publish PUT /api/v1/plugins/admin/:id/publish
func (h *PluginHandler) Publish(c *gin.Context) {
	h.setPublished(c, true)
}

// Unpublish PUT /api/v1/plugins/admin/:id/unpublish
func (h *PluginHandler) Unpublish(c *gin.Context) {
	h.setPublished(c, false)
}

func (h *PluginHandler) setPublished(c *gin.Context, published bool) {
	res, err := db().Exec(`UPDATE plugins SET published = $2, updated_at = NOW() WHERE id = $1`,
		c.Param("id"), published)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update plugin"})
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "plugin not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "updated"})
}

// Delete DELETE /api/v1/plugins/admin/:id — removes the row and the bundle.
func (h *PluginHandler) Delete(c *gin.Context) {
	var path string
	err := db().QueryRow(`SELECT file_path FROM plugins WHERE id = $1`, c.Param("id")).Scan(&path)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "plugin not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load plugin"})
		return
	}
	if _, err := db().Exec(`DELETE FROM plugins WHERE id = $1`, c.Param("id")); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete plugin"})
		return
	}
	if path != "" {
		_ = os.Remove(path)
	}
	c.JSON(http.StatusOK, gin.H{"message": "deleted"})
}

// inspectBundle validates the uploaded zip: manifest present and well-formed,
// entry script exists and within the size cap. Returns the parsed manifest and
// the entry script bytes.
func inspectBundle(buf []byte) (*manifest, []byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(buf), int64(len(buf)))
	if err != nil {
		return nil, nil, fmt.Errorf("not a valid zip bundle")
	}

	var mf *manifest
	var entry []byte
	for _, zf := range zr.File {
		switch strings.ToLower(zf.Name) {
		case "manifest.json":
			rc, err := zf.Open()
			if err != nil {
				return nil, nil, fmt.Errorf("cannot read manifest.json")
			}
			raw, err := io.ReadAll(io.LimitReader(rc, 64<<10))
			rc.Close()
			if err != nil {
				return nil, nil, fmt.Errorf("cannot read manifest.json")
			}
			mf = &manifest{}
			if err := json.Unmarshal(raw, mf); err != nil {
				return nil, nil, fmt.Errorf("manifest.json is not valid JSON")
			}
		default:
			name := strings.TrimPrefix(strings.ToLower(zf.Name), "./")
			if name == strings.ToLower("main.js") || strings.HasSuffix(name, ".js") {
				if entry == nil && !zf.FileInfo().IsDir() {
					rc, err := zf.Open()
					if err == nil {
						entry, _ = io.ReadAll(io.LimitReader(rc, maxEntryBytes+1))
						rc.Close()
					}
				}
			}
		}
	}
	if mf == nil {
		return nil, nil, fmt.Errorf("manifest.json missing from bundle root")
	}

	if !idPattern.MatchString(mf.ID) {
		return nil, nil, fmt.Errorf("invalid plugin id %q (lowercase letters/digits/dots/dashes, 3-64 chars)", mf.ID)
	}
	if strings.TrimSpace(mf.Name) == "" {
		return nil, nil, fmt.Errorf("manifest.name must not be empty")
	}
	if !versionPattern.MatchString(mf.Version) {
		return nil, nil, fmt.Errorf("invalid version %q (expected semver like 1.0.0)", mf.Version)
	}
	if mf.Entry == "" {
		mf.Entry = "main.js"
	}
	if strings.Contains(mf.Entry, "/") || strings.Contains(mf.Entry, "\\") ||
		strings.Contains(mf.Entry, "..") || !strings.HasSuffix(mf.Entry, ".js") {
		return nil, nil, fmt.Errorf("manifest.entry must be a plain .js file name")
	}
	for _, p := range mf.Permissions {
		if !permPattern.MatchString(p) {
			return nil, nil, fmt.Errorf("invalid permission key %q", p)
		}
	}

	// Re-open the declared entry specifically so the cap applies to it.
	for _, zf := range zr.File {
		name := strings.TrimPrefix(zf.Name, "./")
		if strings.EqualFold(name, mf.Entry) {
			rc, err := zf.Open()
			if err != nil {
				return nil, nil, fmt.Errorf("cannot read entry %s", mf.Entry)
			}
			code, err := io.ReadAll(io.LimitReader(rc, maxEntryBytes+1))
			rc.Close()
			if err != nil {
				return nil, nil, fmt.Errorf("cannot read entry %s", mf.Entry)
			}
			if len(code) > maxEntryBytes {
				return nil, nil, fmt.Errorf("entry script too large (max %d bytes)", maxEntryBytes)
			}
			entry = code
			break
		}
	}
	if entry == nil {
		return nil, nil, fmt.Errorf("entry script %s missing from bundle", mf.Entry)
	}
	return mf, entry, nil
}
