package scanner

import (
	"path/filepath"
	"strings"
)

var categoryDefinitions = []FileCategoryDefinition{
	{Category: CategoryPDF, Label: "PDF", Extensions: []string{".pdf"}},
	{Category: CategoryText, Label: "Text", Extensions: []string{".txt", ".md", ".csv", ".tsv", ".json", ".xml", ".html", ".css", ".js", ".go", ".py", ".log", ".ini", ".yaml", ".yml", ".sql", ".ps1"}},
	{Category: CategoryWord, Label: "Word", Extensions: []string{".doc", ".docx", ".docm", ".rtf"}},
	{Category: CategoryExcel, Label: "Excel", Extensions: []string{".xls", ".xlsx", ".xlsm", ".xlsb"}},
	{Category: CategoryPowerPoint, Label: "PowerPoint", Extensions: []string{".ppt", ".pptx", ".pptm"}},
	{Category: CategoryImages, Label: "Images", Extensions: []string{".bmp", ".dib", ".gif", ".jpg", ".jpeg", ".jpe", ".png", ".tif", ".tiff", ".ico", ".heic", ".heif", ".webp", ".raw", ".cr2", ".nef", ".arw"}},
	{Category: CategoryAudio, Label: "Audio", Extensions: []string{".mp3", ".m4a", ".aac", ".wav", ".wma", ".flac", ".ogg", ".aiff"}},
	{Category: CategoryVideo, Label: "Video", Extensions: []string{".mp4", ".m4v", ".mov", ".avi", ".mkv", ".wmv", ".webm", ".mpeg", ".mpg", ".3gp"}},
	{Category: CategoryArchives, Label: "Archives", Extensions: []string{".zip", ".7z", ".rar", ".tar", ".gz", ".bz2", ".xz"}},
	{Category: CategoryOther, Label: "Other", Extensions: nil},
}

var extensionCategories = buildExtensionCategories()

func FileCategoryDefinitions() []FileCategoryDefinition {
	definitions := make([]FileCategoryDefinition, len(categoryDefinitions))
	copy(definitions, categoryDefinitions)
	return definitions
}

func AllFileCategories() map[FileCategory]bool {
	categories := make(map[FileCategory]bool, len(categoryDefinitions))
	for _, definition := range categoryDefinitions {
		categories[definition.Category] = true
	}
	return categories
}

func DefaultScanOptions() ScanOptions {
	return ScanOptions{
		Categories:    AllFileCategories(),
		UserFilesOnly: true,
	}
}

func NormalizeScanOptions(options ScanOptions) ScanOptions {
	options.UserFilesOnly = true
	if len(options.Categories) == 0 {
		options.Categories = AllFileCategories()
		return options
	}

	normalized := make(map[FileCategory]bool, len(options.Categories))
	for _, definition := range categoryDefinitions {
		if options.Categories[definition.Category] {
			normalized[definition.Category] = true
		}
	}
	options.Categories = normalized
	return options
}

func CategoryForPath(path string) FileCategory {
	ext := strings.ToLower(filepath.Ext(path))
	if category, ok := extensionCategories[ext]; ok {
		return category
	}
	return CategoryOther
}

func CategoryLabel(category FileCategory) string {
	for _, definition := range categoryDefinitions {
		if definition.Category == category {
			return definition.Label
		}
	}
	return "Other"
}

func buildExtensionCategories() map[string]FileCategory {
	categories := make(map[string]FileCategory)
	for _, definition := range categoryDefinitions {
		for _, ext := range definition.Extensions {
			categories[strings.ToLower(ext)] = definition.Category
		}
	}
	return categories
}
