package dto

import "time"

// ModelVersion represents a specific version of a model, containing nested model information.
type ModelVersionIdResponse struct {
	ID           int64     `json:"id"`           // The identifier for the model version
	Name         string    `json:"name"`         // The name of the model version
	Description  string    `json:"description"`  // The description of the model version (usually a changelog)
	Model        ModelInfo `json:"model"`        // Embedded information about the parent model
	ModelID      int64     `json:"modelId"`      // The identifier for the parent model
	CreatedAt    time.Time `json:"createdAt"`    // The date in which the version was created
	DownloadURL  string    `json:"downloadUrl"`  // The download url to get the model file for this specific version
	TrainedWords []string  `json:"trainedWords"` // The words used to trigger the model
	Files        []File    `json:"files"`        // The files associated with this model version
	Stats        Stats     `json:"stats"`        // Statistics related to the model
	Images       []Image   `json:"images"`       // The images associated with this model version
}

// ModelInfo contains information about the parent model of a ModelVersion.
type ModelInfo struct {
	Name string  `json:"name"` // The name of the model
	Type string  `json:"type"` // The model type (Checkpoint, TextualInversion, etc.)
	NSFW bool    `json:"nsfw"` // Whether the model is NSFW or not
	POI  bool    `json:"poi"`  // Whether the model is of a person of interest or not
	Mode *string `json:"mode"` // The mode of the model (Archived, TakenDown, or null)
}

// Stats contains statistics about the model.
