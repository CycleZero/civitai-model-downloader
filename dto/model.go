package dto

import (
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ── Request ─────────────────────────────────────────

type ModelRequest struct {
	Limit           *int     `url:"limit,omitempty"`
	Page            *int     `url:"page,omitempty"`
	Cursor          *string  `url:"cursor,omitempty"`
	Query           *string  `url:"query,omitempty"`
	IDs             []int    `url:"ids,omitempty,comma"`
	Tag             *string  `url:"tag,omitempty"`
	Username        *string  `url:"username,omitempty"`
	Types           []string `url:"types,omitempty,comma"`
	BaseModels      []string `url:"baseModels,omitempty,comma"`
	CheckpointType  *string  `url:"checkpointType,omitempty"`
	Sort            *string  `url:"sort,omitempty"`
	Period          *string  `url:"period,omitempty"`
	NSFW            *bool    `url:"nsfw,omitempty"`
	SupportsGen     *bool    `url:"supportsGeneration,omitempty"`
	FromPlatform    *bool    `url:"fromPlatform,omitempty"`
	EarlyAccess     *bool    `url:"earlyAccess,omitempty"`
	PrimaryFileOnly *bool    `url:"primaryFileOnly,omitempty"`
	Favorites       *bool    `url:"favorites,omitempty"`
	Hidden          *bool    `url:"hidden,omitempty"`
}

func (r *ModelRequest) add(p url.Values) {
	if r == nil {
		return
	}
	intPtr(p, "limit", r.Limit)
	intPtr(p, "page", r.Page)
	strPtr(p, "cursor", r.Cursor)
	strPtr(p, "query", r.Query)
	intsJoin(p, "ids", r.IDs)
	strPtr(p, "tag", r.Tag)
	strPtr(p, "username", r.Username)
	strsJoin(p, "types", r.Types)
	strsJoin(p, "baseModels", r.BaseModels)
	strPtr(p, "checkpointType", r.CheckpointType)
	strPtr(p, "sort", r.Sort)
	strPtr(p, "period", r.Period)
	boolPtr(p, "nsfw", r.NSFW)
	boolPtr(p, "supportsGeneration", r.SupportsGen)
	boolPtr(p, "fromPlatform", r.FromPlatform)
	boolPtr(p, "earlyAccess", r.EarlyAccess)
	boolPtr(p, "primaryFileOnly", r.PrimaryFileOnly)
	boolPtr(p, "favorites", r.Favorites)
	boolPtr(p, "hidden", r.Hidden)
}

func (r *ModelRequest) QueryString() string {
	p := url.Values{}
	r.add(p)
	s := p.Encode()
	if s == "" {
		return ""
	}
	return "?" + s
}

func (r *ModelRequest) GetQuery() string { return r.QueryString() }

func (r *ModelRequest) ToMap() map[string]interface{} {
	m := make(map[string]interface{})
	if r == nil {
		return m
	}
	if r.Limit != nil {
		m["limit"] = strconv.Itoa(*r.Limit)
	}
	if r.Page != nil {
		m["page"] = strconv.Itoa(*r.Page)
	}
	if r.Cursor != nil {
		m["cursor"] = *r.Cursor
	}
	if r.Query != nil {
		m["query"] = *r.Query
	}
	if len(r.IDs) > 0 {
		m["ids"] = intsToStr(r.IDs)
	}
	if r.Tag != nil {
		m["tag"] = *r.Tag
	}
	if r.Username != nil {
		m["username"] = *r.Username
	}
	if len(r.Types) > 0 {
		m["types"] = strings.Join(r.Types, ",")
	}
	if len(r.BaseModels) > 0 {
		m["baseModels"] = strings.Join(r.BaseModels, ",")
	}
	if r.CheckpointType != nil {
		m["checkpointType"] = *r.CheckpointType
	}
	if r.Sort != nil {
		m["sort"] = *r.Sort
	}
	if r.Period != nil {
		m["period"] = *r.Period
	}
	if r.NSFW != nil {
		m["nsfw"] = strconv.FormatBool(*r.NSFW)
	}
	if r.SupportsGen != nil {
		m["supportsGeneration"] = strconv.FormatBool(*r.SupportsGen)
	}
	if r.FromPlatform != nil {
		m["fromPlatform"] = strconv.FormatBool(*r.FromPlatform)
	}
	if r.EarlyAccess != nil {
		m["earlyAccess"] = strconv.FormatBool(*r.EarlyAccess)
	}
	if r.PrimaryFileOnly != nil {
		m["primaryFileOnly"] = strconv.FormatBool(*r.PrimaryFileOnly)
	}
	if r.Favorites != nil {
		m["favorites"] = strconv.FormatBool(*r.Favorites)
	}
	if r.Hidden != nil {
		m["hidden"] = strconv.FormatBool(*r.Hidden)
	}
	return m
}

// ── Helpers ────────────────────────────────────────

func intPtr(p url.Values, k string, v *int) {
	if v != nil {
		p.Set(k, strconv.Itoa(*v))
	}
}

func strPtr(p url.Values, k string, v *string) {
	if v != nil {
		p.Set(k, *v)
	}
}

func boolPtr(p url.Values, k string, v *bool) {
	if v != nil {
		p.Set(k, strconv.FormatBool(*v))
	}
}

func intsJoin(p url.Values, k string, v []int) {
	if len(v) > 0 {
		p.Set(k, intsToStr(v))
	}
}

func strsJoin(p url.Values, k string, v []string) {
	if len(v) > 0 {
		p.Set(k, strings.Join(v, ","))
	}
}

func intsToStr(v []int) string {
	var b strings.Builder
	for i, n := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.Itoa(n))
	}
	return b.String()
}

// ── Response: GET /api/v1/models ────────────────────

type ModelsResponse struct {
	Items    []ModelItem `json:"items"`
	Metadata *Metadata   `json:"metadata,omitempty"`
}

// ── Model item (list & GET /models/{id}) ────────────

type ModelItem struct {
	ID                    int                    `json:"id"`
	Name                  string                 `json:"name"`
	Description           string                 `json:"description"`
	Type                  string                 `json:"type"`
	NSFW                  bool                   `json:"nsfw"`
	NSFWLevel             int                    `json:"nsfwLevel"`
	Availability          string                 `json:"availability"`
	SupportsGeneration    bool                   `json:"supportsGeneration"`
	AllowNoCredit         bool                   `json:"allowNoCredit"`
	AllowCommercialUse    string                 `json:"allowCommercialUse"`
	AllowDerivatives      bool                   `json:"allowDerivatives"`
	AllowDifferentLicense bool                   `json:"allowDifferentLicense"`
	Minor                 bool                   `json:"minor"`
	POI                   bool                   `json:"poi"`
	SFWOnly               bool                   `json:"sfwOnly"`
	Mode                  *string                `json:"mode"`
	Stats                 *ModelStats            `json:"stats,omitempty"`
	Creator               *Creator               `json:"creator,omitempty"`
	Tags                  []string               `json:"tags"`
	ModelVersions         []ModelVersionCompact  `json:"modelVersions"`
}

// ── Compact model version (inside list) ─────────────

type ModelVersionCompact struct {
	ID                 int           `json:"id"`
	Name               string        `json:"name"`
	BaseModel          string        `json:"baseModel"`
	BaseModelType      string        `json:"baseModelType"`
	PublishedAt        *time.Time    `json:"publishedAt"`
	SupportsGeneration bool          `json:"supportsGeneration"`
	Stats              *VersionStats `json:"stats,omitempty"`
	Files              []File        `json:"files"`
	Images             []Image       `json:"images"`
	DownloadURL        string        `json:"downloadUrl"`
}

// ── File ────────────────────────────────────────────

type File struct {
	ID               int           `json:"id"`
	Name             string        `json:"name"`
	Type             string        `json:"type"`
	SizeKB           float64       `json:"sizeKB"`
	Metadata         *FileMetadata `json:"metadata,omitempty"`
	PickleScanResult string        `json:"pickleScanResult"`
	VirusScanResult  string        `json:"virusScanResult"`
	Hashes           *FileHashes   `json:"hashes,omitempty"`
	DownloadURL      string        `json:"downloadUrl"`
	Primary          bool          `json:"primary"`
}

type FileHashes struct {
	AutoV1 string `json:"AutoV1,omitempty"`
	AutoV2 string `json:"AutoV2,omitempty"`
	AutoV3 string `json:"AutoV3,omitempty"`
	SHA256 string `json:"SHA256,omitempty"`
	CRC32  string `json:"CRC32,omitempty"`
	BLAKE3 string `json:"BLAKE3,omitempty"`
}

type FileMetadata struct {
	Format string  `json:"format,omitempty"`
	Size   *string `json:"size"`
	FP     *string `json:"fp"`
}

// ── Image ───────────────────────────────────────────

type Image struct {
	ID     int    `json:"id"`
	URL    string `json:"url"`
	NSFW   string `json:"nsfw"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Hash   string `json:"hash"`
	Type   string `json:"type"`
}

// ── Creator ─────────────────────────────────────────

type Creator struct {
	Username string  `json:"username"`
	Image    *string `json:"image"`
}

// ── Stats ───────────────────────────────────────────

type ModelStats struct {
	DownloadCount     int `json:"downloadCount"`
	ThumbsUpCount     int `json:"thumbsUpCount"`
	ThumbsDownCount   int `json:"thumbsDownCount"`
	CommentCount      int `json:"commentCount"`
	TippedAmountCount int `json:"tippedAmountCount"`
}

type VersionStats struct {
	DownloadCount   int `json:"downloadCount"`
	ThumbsUpCount   int `json:"thumbsUpCount"`
	ThumbsDownCount int `json:"thumbsDownCount"`
}

// ── Metadata (pagination) ──────────────────────────

type Metadata struct {
	NextCursor  string `json:"nextCursor,omitempty"`
	NextPage    string `json:"nextPage,omitempty"`
	CurrentPage int    `json:"currentPage,omitempty"`
	PageSize    int    `json:"pageSize,omitempty"`
}

// ── Model info (nested in full version) ─────────────

type ModelInfo struct {
	Name string  `json:"name"`
	Type string  `json:"type"`
	NSFW bool    `json:"nsfw"`
	POI  bool    `json:"poi"`
	Mode *string `json:"mode"`
}

// ── Enums ──────────────────────────────────────────

type EnumsResponse struct {
	ModelType       []string `json:"ModelType"`
	ModelFileType   []string `json:"ModelFileType"`
	ActiveBaseModel []string `json:"ActiveBaseModel"`
	BaseModel       []string `json:"BaseModel"`
	BaseModelType   []string `json:"BaseModelType"`
}

// ── Compat aliases ──────────────────────────────────

type ModelResponse = ModelsResponse
type ModelIdResponse = ModelItem
