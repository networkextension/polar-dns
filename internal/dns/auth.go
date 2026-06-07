package dns

// auth.go — dns-svc has no session store of its own; it asks dock to
// introspect Bearer tokens via /internal/v1/auth/verify (cached 30s in
// the SDK). Mirrors polar-hosts/internal/hosts/auth.go.

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
)

const (
	ctxKeyUserID      = "user_id"
	ctxKeyUserRole    = "user_role"
	ctxKeyWorkspaceID = "workspace_id"
)

// requireAdminViaDock extracts Bearer → Dock.AuthVerify → role=admin.
// Sets user_id / user_role / workspace_id on the gin context.
func (p *Plugin) requireAdminViaDock() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := extractAccessToken(c)
		if token == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing bearer token"})
			return
		}
		res, err := p.Dock.AuthVerifyWS(token, strings.TrimSpace(c.GetHeader("X-Workspace-Id")))
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid session"})
			return
		}
		if !strings.EqualFold(res.Role, "admin") {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "admin role required"})
			return
		}
		c.Set(ctxKeyUserID, res.UserID)
		c.Set(ctxKeyUserRole, res.Role)
		c.Set(ctxKeyWorkspaceID, p.resolveActiveWorkspace(c, res.WorkspaceID, res.UserID))
		c.Next()
	}
}

// requireAuthViaDock — same Bearer + AuthVerify pattern, any role, plus
// the closed-by-default workspace plugin-access gate.
func (p *Plugin) requireAuthViaDock() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := extractAccessToken(c)
		if token == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing bearer token"})
			return
		}
		res, err := p.Dock.AuthVerifyWS(token, strings.TrimSpace(c.GetHeader("X-Workspace-Id")))
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid session"})
			return
		}
		// Scope to the SELECTED workspace (X-Workspace-Id), not the
		// personal-team default, so DNS resources scope correctly.
		wsID := p.resolveActiveWorkspace(c, res.WorkspaceID, res.UserID)
		c.Set(ctxKeyUserID, res.UserID)
		c.Set(ctxKeyUserRole, res.Role)
		c.Set(ctxKeyWorkspaceID, wsID)

		// Closed-by-default tenant access gate. Fail-closed on error.
		access, err := p.Dock.WorkspacePluginAccess(wsID, p.Name)
		if err != nil || access == nil {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "plugin access check failed"})
			return
		}
		if !access.Enabled {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "workspace not granted access to dns"})
			return
		}
		c.Next()
	}
}

// resolveActiveWorkspace picks the workspace this request operates in:
// the caller's X-Workspace-Id when they're a member, else their personal
// workspace from AuthVerify. Mirrors dock's own resolution.
func (p *Plugin) resolveActiveWorkspace(c *gin.Context, personalWS, userID string) string {
	return resolveWorkspaceID(c.GetHeader("X-Workspace-Id"), personalWS, userID, p.userIsTeamMember)
}

// resolveWorkspaceID is the pure decision (no I/O), unit-testable: the
// requested workspace wins only when non-empty, different from personal,
// and the user is a member; otherwise personal. Never 403s — a foreign
// X-Workspace-Id silently falls back to personal (no cross-tenant leak).
func resolveWorkspaceID(requested, personalWS, userID string, isMember func(teamID, userID string) bool) string {
	requested = strings.TrimSpace(requested)
	if requested == "" || requested == personalWS {
		return personalWS
	}
	if isMember != nil && isMember(requested, userID) {
		return requested
	}
	return personalWS
}

// userIsTeamMember asks dock whether userID belongs to teamID via
// GET /internal/v1/teams/:id/members/:user (200 = member). Error → false.
func (p *Plugin) userIsTeamMember(teamID, userID string) bool {
	teamID = strings.TrimSpace(teamID)
	userID = strings.TrimSpace(userID)
	if teamID == "" || userID == "" {
		return false
	}
	resp, err := p.Dock.Do(http.MethodGet,
		"/internal/v1/teams/"+url.PathEscape(teamID)+"/members/"+url.PathEscape(userID), nil)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// extractAccessToken: Bearer header → ?access_token= → cookie.
func extractAccessToken(c *gin.Context) string {
	if v := strings.TrimSpace(c.GetHeader("Authorization")); v != "" {
		if strings.HasPrefix(strings.ToLower(v), "bearer ") {
			return strings.TrimSpace(v[7:])
		}
	}
	if v := strings.TrimSpace(c.Query("access_token")); v != "" {
		return v
	}
	if v, err := c.Cookie("access_token"); err == nil && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return ""
}
