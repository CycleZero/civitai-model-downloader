package dto

// Model represents the main model structure returned by the API
type ModelIdResponse struct {
	ID            int64          `json:"id"`            // The identifier for the model
	Name          string         `json:"name"`          // The name of the model
	Description   string         `json:"description"`   // The description of the model (HTML)
	Type          string         `json:"type"`          // The model type (Checkpoint, TextualInversion, Hypernetwork, AestheticGradient, LORA, Controlnet, Poses)
	NSFW          bool           `json:"nsfw"`          // Whether the model is NSFW or not
	Tags          []string       `json:"tags"`          // The tags associated with the model
	Mode          *string        `json:"mode"`          // The mode in which the model is currently on. If Archived, files field will be empty. If TakenDown, images field will be empty
	Creator       Creator        `json:"creator"`       // The creator of the model
	ModelVersions []ModelVersion `json:"modelVersions"` // The versions of the model
}

// Creator represents the creator of the model
type Creator struct {
	Username string  `json:"username"` // The name of the creator
	Image    *string `json:"image"`    // The url of the creator's avatar
}
