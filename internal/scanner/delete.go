package scanner

import (
	"os"

	trash "github.com/hymkor/trash-go"
)

var trashThrow = trash.Throw

func DeleteFiles(paths []string, permanentFallback bool) DeleteResult {
	result := DeleteResult{
		Deleted: make([]DeletedFile, 0, len(paths)),
		Failed:  make([]DeleteFailure, 0),
	}

	for _, path := range paths {
		if path == "" {
			continue
		}

		if err := trashThrow(path); err == nil {
			result.Deleted = append(result.Deleted, DeletedFile{Path: path, Action: DeleteActionTrash})
			continue
		}

		if permanentFallback {
			if err := os.Remove(path); err == nil {
				result.Deleted = append(result.Deleted, DeletedFile{Path: path, Action: DeleteActionPermanent})
				continue
			} else {
				result.Failed = append(result.Failed, DeleteFailure{Path: path, Error: err.Error()})
				continue
			}
		}

		result.Failed = append(result.Failed, DeleteFailure{Path: path, Error: "trash unavailable"})
	}

	return result
}
