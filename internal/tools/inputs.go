package tools

// Input types for the parity tools. The official SDK infers each tool's JSON
// input schema from these structs (field names from `json` tags, descriptions
// from `jsonschema` tags). Optional args that have a non-zero editor default use
// pointers so "omitted" is distinguishable from "zero" (e.g. spawn z default 100).

type noArgs struct{}

type actorLabelIn struct {
	ActorLabel string `json:"actor_label" jsonschema:"the actor's editor label"`
}

type listAssetsIn struct {
	Path      string `json:"path,omitempty" jsonschema:"content path, default /Game"`
	Recursive *bool  `json:"recursive,omitempty" jsonschema:"recurse subfolders; default true"`
	Limit     *int   `json:"limit,omitempty" jsonschema:"max results; default 200"`
}

type importAssetsIn struct {
	FilePaths       []string `json:"file_paths" jsonschema:"external files (FBX/textures/audio) to import from disk"`
	DestinationPath string   `json:"destination_path,omitempty" jsonschema:"content destination, default /Game/Imported"`
}

type takeScreenshotIn struct {
	Width             int       `json:"width,omitempty" jsonschema:"default 1280"`
	Height            int       `json:"height,omitempty" jsonschema:"default 720"`
	CameraLocation    []float64 `json:"camera_location,omitempty" jsonschema:"[x,y,z]; defaults to the viewport camera"`
	CameraRotationPyr []float64 `json:"camera_rotation_pyr,omitempty" jsonschema:"[pitch,yaw,roll]"`
}

type startPlayIn struct {
	Simulate bool `json:"simulate,omitempty" jsonschema:"run the world without possessing a player pawn"`
}
