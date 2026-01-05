package dto

import (
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// ModelRequest represents the query parameters for the GET /api/v1/models endpoint.
// All fields are pointers to handle optional parameters correctly.
type ModelRequest struct {
	Limit                  *int     `url:"limit,omitempty"`
	Page                   *int     `url:"page,omitempty"`
	Query                  *string  `url:"query,omitempty"`
	Tag                    *string  `url:"tag,omitempty"`
	Username               *string  `url:"username,omitempty"`
	Types                  []string `url:"types,omitempty,comma"` // enum[]
	Sort                   *string  `url:"sort,omitempty"`        // enum
	Period                 *string  `url:"period,omitempty"`      // enum
	Rating                 *int     `url:"rating,omitempty"`      // Deprecated
	Favorites              *bool    `url:"favorites,omitempty"`
	Hidden                 *bool    `url:"hidden,omitempty"`
	PrimaryFileOnly        *bool    `url:"primaryFileOnly,omitempty"`
	AllowNoCredit          *bool    `url:"allowNoCredit,omitempty"`
	AllowDerivatives       *bool    `url:"allowDerivatives,omitempty"`
	AllowDifferentLicenses *bool    `url:"allowDifferentLicenses,omitempty"`
	AllowCommercialUse     []string `url:"allowCommercialUse,omitempty,comma"` // enum[]
	NSFW                   *bool    `url:"nsfw,omitempty"`
	SupportsGeneration     *bool    `url:"supportsGeneration,omitempty"`
	IDs                    []int    `url:"ids,omitempty,comma"`
	BaseModels             []string `url:"baseModels,omitempty,comma"`
}

func (m *ModelRequest) GetQuery() string {
	return buildQuery(m)
}

func (m *ModelRequest) ToMap() map[string]interface{} {
	result := make(map[string]interface{})

	v := reflect.ValueOf(m).Elem()
	t := reflect.TypeOf(m).Elem()

	for i := 0; i < v.NumField(); i++ {
		field := v.Field(i)
		typeField := t.Field(i)

		// Get the tag for the field
		tag := typeField.Tag.Get("url")
		if tag == "" {
			continue
		}

		// Parse tag options
		tagParts := strings.Split(tag, ",")
		key := tagParts[0]
		hasOmitempty := false
		for _, part := range tagParts[1:] {
			if part == "omitempty" {
				hasOmitempty = true
			}
		}

		if key == "" {
			key = strings.ToLower(typeField.Name)
		}

		// Skip if field is zero value and omitempty is set
		if hasOmitempty {
			if field.Kind() == reflect.Ptr && field.IsNil() {
				continue
			} else if field.Kind() == reflect.Slice && field.Len() == 0 {
				continue
			} else if field.Kind() != reflect.Ptr && field.Kind() != reflect.Slice && field.IsZero() {
				continue
			}
		}

		// Handle different field types
		if field.Kind() == reflect.Ptr && !field.IsNil() {
			// Dereference pointer and convert to string
			elem := field.Elem()
			switch elem.Kind() {
			case reflect.String:
				result[key] = elem.String()
			case reflect.Int, reflect.Int32, reflect.Int64:
				result[key] = strconv.FormatInt(elem.Int(), 10)
			case reflect.Bool:
				result[key] = strconv.FormatBool(elem.Bool())
			}
		} else if field.Kind() == reflect.Slice {
			// Handle slice types
			if field.Len() > 0 {
				if strings.Contains(tag, "comma") {
					// Comma-separated values
					var values []string
					for j := 0; j < field.Len(); j++ {
						elem := field.Index(j)
						switch elem.Kind() {
						case reflect.String:
							values = append(values, elem.String())
						case reflect.Int, reflect.Int32, reflect.Int64:
							values = append(values, strconv.FormatInt(elem.Int(), 10))
						}
					}
					result[key] = strings.Join(values, ",")
				} else {
					// For non-comma slices, we return the slice as is
					var values []interface{}
					for j := 0; j < field.Len(); j++ {
						elem := field.Index(j)
						switch elem.Kind() {
						case reflect.String:
							values = append(values, elem.String())
						case reflect.Int, reflect.Int32, reflect.Int64:
							values = append(values, strconv.FormatInt(elem.Int(), 10))
						}
					}
					result[key] = values
				}
			}
		} else if field.Kind() != reflect.Ptr {
			// Handle non-pointer values
			switch field.Kind() {
			case reflect.String:
				result[key] = field.String()
			case reflect.Int, reflect.Int32, reflect.Int64:
				result[key] = strconv.FormatInt(field.Int(), 10)
			case reflect.Bool:
				result[key] = strconv.FormatBool(field.Bool())
			}
		}
	}

	return result
}

// buildQuery builds a query string from a struct using URL tags
func buildQuery(req interface{}) string {
	v := reflect.ValueOf(req)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	t := v.Type()

	params := url.Values{}

	for i := 0; i < v.NumField(); i++ {
		field := v.Field(i)
		typeField := t.Field(i)

		// Get the tag for the field
		tag := typeField.Tag.Get("url")
		if tag == "" {
			continue
		}

		// Parse tag options
		tagParts := strings.Split(tag, ",")
		key := tagParts[0]
		hasOmitempty := false
		hasComma := false
		for _, part := range tagParts[1:] {
			if part == "omitempty" {
				hasOmitempty = true
			} else if part == "comma" {
				hasComma = true
			}
		}

		if key == "" {
			key = strings.ToLower(typeField.Name)
		}

		// Skip if field is zero value and omitempty is set
		if hasOmitempty {
			if field.Kind() == reflect.Ptr && field.IsNil() {
				continue
			} else if field.Kind() == reflect.Slice && field.Len() == 0 {
				continue
			} else if field.Kind() != reflect.Ptr && field.Kind() != reflect.Slice && field.IsZero() {
				continue
			}
		}

		// Handle different field types
		if field.Kind() == reflect.Ptr && !field.IsNil() {
			// Dereference pointer and convert to string
			elem := field.Elem()
			var value string
			switch elem.Kind() {
			case reflect.String:
				value = elem.String()
			case reflect.Int, reflect.Int32, reflect.Int64:
				value = strconv.FormatInt(elem.Int(), 10)
			case reflect.Bool:
				value = strconv.FormatBool(elem.Bool())
			default:
				continue
			}
			params.Add(key, value)
		} else if field.Kind() == reflect.Slice {
			// Handle slice types
			if field.Len() > 0 {
				if hasComma {
					// Comma-separated values
					var values []string
					for j := 0; j < field.Len(); j++ {
						elem := field.Index(j)
						var elemValue string
						switch elem.Kind() {
						case reflect.String:
							elemValue = elem.String()
						case reflect.Int, reflect.Int32, reflect.Int64:
							elemValue = strconv.FormatInt(elem.Int(), 10)
						default:
							continue
						}
						values = append(values, elemValue)
					}
					if len(values) > 0 {
						params.Add(key, strings.Join(values, ","))
					}
				} else {
					// Multiple values with the same key
					for j := 0; j < field.Len(); j++ {
						elem := field.Index(j)
						var elemValue string
						switch elem.Kind() {
						case reflect.String:
							elemValue = elem.String()
						case reflect.Int, reflect.Int32, reflect.Int64:
							elemValue = strconv.FormatInt(elem.Int(), 10)
						default:
							continue
						}
						params.Add(key, elemValue)
					}
				}
			}
		} else if field.Kind() != reflect.Ptr {
			// Handle non-pointer values
			var value string
			switch field.Kind() {
			case reflect.String:
				value = field.String()
			case reflect.Int, reflect.Int32, reflect.Int64:
				value = strconv.FormatInt(field.Int(), 10)
			case reflect.Bool:
				value = strconv.FormatBool(field.Bool())
			default:
				continue
			}
			params.Add(key, value)
		}
	}

	return params.Encode()
}

// ModelResponse represents the entire JSON response from the GET /api/v1/models endpoint.
type ModelResponse struct {
	ID            int64          `json:"id"`
	Name          string         `json:"name"`
	Description   string         `json:"description"`
	Type          string         `json:"type"`
	NSFW          bool           `json:"nsfw"`
	Tags          []string       `json:"tags"`
	Mode          *string        `json:"mode"`
	Creator       Creator        `json:"creator"`
	Stats         Stats          `json:"stats"`
	ModelVersions []ModelVersion `json:"modelVersions"`
	Metadata      Metadata       `json:"metadata"`
}

// Creator contains information about the model's creator.

// Stats contains statistics about the model.
type Stats struct {
	DownloadCount int64   `json:"downloadCount"`
	FavoriteCount int64   `json:"favoriteCount"`
	CommentCount  int64   `json:"commentCount"`
	RatingCount   int64   `json:"ratingCount"`
	Rating        float64 `json:"rating"`
}

// ModelVersion represents a specific version of the model.
type ModelVersion struct {
	ID           int64             `json:"id"`
	Name         string            `json:"name"`
	Description  string            `json:"description"`
	CreatedAt    time.Time         `json:"createdAt" time_format:"2006-01-02T15:04:05Z07:00"`
	DownloadURL  string            `json:"downloadUrl"`
	TrainedWords []string          `json:"trainedWords"`
	Files        []File            `json:"files"`
	Images       []Image           `json:"images"`
	Stats        ModelVersionStats `json:"stats"`
}

// File represents a file associated with a model version.
type File struct {
	SizeKb           float64       `json:"sizeKb"`
	PickleScanResult string        `json:"pickleScanResult"`
	VirusScanResult  string        `json:"virusScanResult"`
	ScannedAt        *time.Time    `json:"scannedAt" time_format:"2006-01-02T15:04:05Z07:00"`
	Primary          *bool         `json:"primary"`
	Metadata         *FileMetadata `json:"metadata"`
}

// FileMetadata contains metadata about a model file.
type FileMetadata struct {
	FP     *string `json:"fp"`
	Size   *string `json:"size"`
	Format *string `json:"format"`
}

// Image represents an image associated with a model version.
type Image struct {
	ID     string                  `json:"id"`
	URL    string                  `json:"url"`
	NSFW   string                  `json:"nsfw"`
	Width  int64                   `json:"width"`
	Height int64                   `json:"height"`
	Hash   string                  `json:"hash"`
	Meta   *map[string]interface{} `json:"meta"`
}

// ModelVersionStats contains statistics specific to a model version.
type ModelVersionStats struct {
	DownloadCount int64   `json:"downloadCount"`
	RatingCount   int64   `json:"ratingCount"`
	Rating        float64 `json:"rating"`
}

// Metadata contains pagination information for the response.
type Metadata struct {
	TotalItems  int     `json:"totalItems,string"`
	CurrentPage int     `json:"currentPage,string"`
	PageSize    int     `json:"pageSize,string"`
	TotalPages  int     `json:"totalPages,string"`
	NextPage    *string `json:"nextPage"`
	PrevPage    *string `json:"prevPage"`
}
