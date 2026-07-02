package tools

// Input types for the parity tools. The official SDK infers each tool's JSON
// input schema from these structs (field names from `json` tags, descriptions
// from `jsonschema` tags). Optional args that have a non-zero editor default use
// pointers so "omitted" is distinguishable from "zero" (e.g. spawn z default 100).

type noArgs struct{}

type executePythonIn struct {
	Code     string `json:"code" jsonschema:"Python code to run in the editor (full unreal module access)"`
	Evaluate bool   `json:"evaluate,omitempty" jsonschema:"evaluate a single expression and return its value instead of running a script"`
}

type consoleIn struct {
	Command string `json:"command" jsonschema:"the console command, e.g. 'stat fps', 'r.ScreenPercentage 50', 'LiveCoding.Compile'"`
}

type openLevelIn struct {
	LevelPath string `json:"level_path" jsonschema:"level asset path, e.g. /Game/Maps/L_Arena (unsaved changes are saved first)"`
}

type listActorsIn struct {
	NameFilter string `json:"name_filter,omitempty" jsonschema:"case-insensitive filter matching actor label or class name"`
}

type actorLabelIn struct {
	ActorLabel string `json:"actor_label" jsonschema:"the actor's editor label"`
}

type spawnActorIn struct {
	ClassPath      string   `json:"class_path" jsonschema:"native class (/Script/Engine.PointLight) or Blueprint asset path (/Game/BP_Thing); for a mesh prop use /Script/Engine.StaticMeshActor + static_mesh_path"`
	X              float64  `json:"x,omitempty"`
	Y              float64  `json:"y,omitempty"`
	Z              *float64 `json:"z,omitempty" jsonschema:"Z location; defaults to 100 when omitted"`
	Pitch          float64  `json:"pitch,omitempty"`
	Yaw            float64  `json:"yaw,omitempty"`
	Roll           float64  `json:"roll,omitempty"`
	Label          string   `json:"label,omitempty"`
	StaticMeshPath string   `json:"static_mesh_path,omitempty" jsonschema:"e.g. /Engine/BasicShapes/Cube for a StaticMeshActor"`
}

type setTransformIn struct {
	ActorLabel  string    `json:"actor_label"`
	Location    []float64 `json:"location,omitempty" jsonschema:"[x,y,z]"`
	RotationPyr []float64 `json:"rotation_pyr,omitempty" jsonschema:"[pitch,yaw,roll]"`
	Scale       []float64 `json:"scale,omitempty" jsonschema:"[x,y,z]"`
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
