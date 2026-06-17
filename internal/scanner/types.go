package scanner

import "time"

type Stage string

const (
	StageIdle            Stage = "Idle"
	StageSurfaceScan     Stage = "Surface Scan"
	StageSizeMapping     Stage = "Size Mapping"
	StageBoundaryHashing Stage = "Boundary Hashing"
	StageFullHashing     Stage = "Full Hashing"
	StageDone            Stage = "Done"
)

type FileCategory string

const (
	CategoryPDF        FileCategory = "pdf"
	CategoryText       FileCategory = "text"
	CategoryWord       FileCategory = "word"
	CategoryExcel      FileCategory = "excel"
	CategoryPowerPoint FileCategory = "powerpoint"
	CategoryImages     FileCategory = "images"
	CategoryAudio      FileCategory = "audio"
	CategoryVideo      FileCategory = "video"
	CategoryArchives   FileCategory = "archives"
	CategoryOther      FileCategory = "other"
)

type FileRecord struct {
	Path       string
	Size       int64
	CreatedAt  time.Time
	ModifiedAt time.Time
	Category   FileCategory
}

type DuplicateGroup struct {
	Size  int64
	Hash  string
	Files []FileRecord
}

type Progress struct {
	Stage              Stage
	CurrentPath        string
	FilesProcessed     int64
	FilesTotal         int64
	DirectoriesScanned int64
	BytesHashed        int64
	GroupsFound        int
	ErrorsIgnored      int64
	SkippedSystemItems int64
	StartedAt          time.Time
	Message            string
}

type ScanOptions struct {
	Categories    map[FileCategory]bool
	UserFilesOnly bool
}

type FileCategoryDefinition struct {
	Category   FileCategory
	Label      string
	Extensions []string
}

type SurfaceCategoryStats struct {
	Files int64
	Bytes int64
}

type SurfaceReport struct {
	Files              []FileRecord
	CategoryStats      map[FileCategory]SurfaceCategoryStats
	TotalFiles         int64
	TotalBytes         int64
	DirectoriesScanned int64
	ErrorsIgnored      int64
	SkippedSystemItems int64
}

type DeleteAction string

const (
	DeleteActionTrash     DeleteAction = "trash"
	DeleteActionPermanent DeleteAction = "permanent"
)

type DeletedFile struct {
	Path   string
	Action DeleteAction
}

type DeleteFailure struct {
	Path  string
	Error string
}

type DeleteResult struct {
	Deleted []DeletedFile
	Failed  []DeleteFailure
}
