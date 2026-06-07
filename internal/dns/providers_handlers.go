package dns

// providers_handlers.go — /api/dns/providers CRUD. Credentials are taken
// as a provider-neutral map and never echoed back. Create validates the
// type + credential shape + proxy scheme by constructing a live Provider
// (no network call) before persisting.

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

type createProviderReq struct {
	ProviderType string            `json:"provider_type"`
	Name         string            `json:"name"`
	Credential   map[string]string `json:"credential"`
	ProxyURL     string            `json:"proxy_url"`
}

func (p *Plugin) handleProviderCreate(c *gin.Context) {
	wsID := c.GetString(ctxKeyWorkspaceID)
	userID := c.GetString(ctxKeyUserID)

	var req createProviderReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body: " + err.Error()})
		return
	}
	req.ProviderType = strings.TrimSpace(req.ProviderType)
	req.Name = strings.TrimSpace(req.Name)
	if req.ProviderType == "" || req.Name == "" || len(req.Credential) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "provider_type, name, and credential are required"})
		return
	}

	// Validate type + credential shape + proxy scheme up front (no network).
	if _, err := NewProvider(req.ProviderType, req.Credential, req.ProxyURL); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	cipher, plain, encrypted, err := p.sealCredential(req.Credential)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "seal credential: " + err.Error()})
		return
	}

	a := ProviderAccount{
		ID:           newID("dp_"),
		WorkspaceID:  wsID,
		Name:         req.Name,
		ProviderType: req.ProviderType,
		ProxyURL:     strings.TrimSpace(req.ProxyURL),
		Encrypted:    encrypted,
		CreatedBy:    userID,
		credCipher:   cipher,
		credPlain:    plain,
	}
	if err := p.insertProvider(c.Request.Context(), a); err != nil {
		if isUniqueViolation(err) {
			c.JSON(http.StatusConflict, gin.H{"error": "a provider named " + req.Name + " already exists in this workspace"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	p.writeAudit(c.Request.Context(), wsID, userID, "provider.add", a.ID, nil, gin.H{
		"name": a.Name, "provider_type": a.ProviderType, "encrypted": a.Encrypted,
	})
	c.JSON(http.StatusCreated, a)
}

type updateProviderReq struct {
	Name       *string           `json:"name"`
	ProxyURL   *string           `json:"proxy_url"`
	Credential map[string]string `json:"credential"` // omit/empty = keep existing
}

// handleProviderUpdate edits a provider's name / proxy / credential.
// provider_type is immutable. Credential, when supplied, must be the full
// set for that type (it replaces, not merges). Re-validates the effective
// config (no network) and re-seals if the credential changed.
func (p *Plugin) handleProviderUpdate(c *gin.Context) {
	ctx := c.Request.Context()
	wsID := c.GetString(ctxKeyWorkspaceID)
	userID := c.GetString(ctxKeyUserID)
	id := strings.TrimSpace(c.Param("id"))

	var req updateProviderReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body: " + err.Error()})
		return
	}
	acct, err := p.getProvider(ctx, wsID, id)
	if errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "provider not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	name := acct.Name
	if req.Name != nil {
		name = strings.TrimSpace(*req.Name)
		if name == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "name cannot be empty"})
			return
		}
	}
	proxy := acct.ProxyURL
	if req.ProxyURL != nil {
		proxy = strings.TrimSpace(*req.ProxyURL)
	}

	changingCred := len(req.Credential) > 0
	effCred := req.Credential
	if !changingCred {
		ec, err := p.openCredential(acct)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "decrypt existing credential: " + err.Error()})
			return
		}
		effCred = ec
	}

	// Validate the effective config (type + credential shape + proxy scheme).
	if _, err := NewProvider(acct.ProviderType, effCred, proxy); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	upd := acct
	upd.Name = name
	upd.ProxyURL = proxy
	if changingCred {
		cipher, plain, encrypted, err := p.sealCredential(effCred)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "seal credential: " + err.Error()})
			return
		}
		upd.credCipher, upd.credPlain, upd.Encrypted = cipher, plain, encrypted
	}

	ok, err := p.updateProvider(ctx, upd)
	if err != nil {
		if isUniqueViolation(err) {
			c.JSON(http.StatusConflict, gin.H{"error": "a provider named " + name + " already exists in this workspace"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "provider not found"})
		return
	}
	p.writeAudit(ctx, wsID, userID, "provider.update", id,
		gin.H{"name": acct.Name, "proxy_url": acct.ProxyURL},
		gin.H{"name": upd.Name, "proxy_url": upd.ProxyURL, "cred_rotated": changingCred})
	c.JSON(http.StatusOK, upd)
}

func (p *Plugin) handleProviderList(c *gin.Context) {
	wsID := c.GetString(ctxKeyWorkspaceID)
	list, err := p.listProviders(c.Request.Context(), wsID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"providers": list})
}

func (p *Plugin) handleProviderDelete(c *gin.Context) {
	wsID := c.GetString(ctxKeyWorkspaceID)
	userID := c.GetString(ctxKeyUserID)
	id := strings.TrimSpace(c.Param("id"))

	ok, err := p.deleteProvider(c.Request.Context(), wsID, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "provider not found"})
		return
	}
	p.writeAudit(c.Request.Context(), wsID, userID, "provider.delete", id, nil, nil)
	c.JSON(http.StatusOK, gin.H{"deleted": true})
}
