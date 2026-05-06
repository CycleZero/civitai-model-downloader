package dto

import (
	"encoding/json"
	"time"
)

// ModelVersionFull is the response from:
//
//	GET /api/v1/model-versions/{id}
//	GET /api/v1/model-versions/by-hash/{hash}
type ModelVersionFull struct {
	ID                int              `json:"id"`
	ModelID           int              `json:"modelId"`
	Name              string           `json:"name"`
	Description       *string          `json:"description"`
	BaseModel         string           `json:"baseModel"`
	BaseModelType     string           `json:"baseModelType"`
	AIR               string           `json:"air"`
	Status            string           `json:"status"`
	Availability      string           `json:"availability"`
	NSFWLevel         int              `json:"nsfwLevel"`
	CreatedAt         time.Time        `json:"createdAt"`
	UpdatedAt         time.Time        `json:"updatedAt"`
	PublishedAt       time.Time        `json:"publishedAt"`
	UploadType        string           `json:"uploadType"`
	UsageControl      string           `json:"usageControl"`
	TrainedWords      []string         `json:"trainedWords"`
	EarlyAccessConfig *json.RawMessage `json:"earlyAccessConfig"`
	EarlyAccessEndsAt *time.Time       `json:"earlyAccessEndsAt"`
	TrainingStatus    *string          `json:"trainingStatus"`
	TrainingDetails   *json.RawMessage `json:"trainingDetails"`
	Stats             *VersionStats    `json:"stats,omitempty"`
	Model             *ModelInfo       `json:"model,omitempty"`
	Files             []File           `json:"files"`
	Images            []Image          `json:"images"`
	DownloadURL       string           `json:"downloadUrl"`
}

// ModelVersionIdResponse is the old name, kept for backward compat.
type ModelVersionIdResponse = ModelVersionFull
