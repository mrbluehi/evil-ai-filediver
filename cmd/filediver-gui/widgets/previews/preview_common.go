package previews

import (
	"github.com/xypwn/filediver/stingray"
)

type GetResourceFunc func(id stingray.FileID, typ stingray.DataType) (data []byte, exists bool, err error)
type GetResourceGeneratorFunc func(allowOverrides bool) func(id stingray.FileID, typ stingray.DataType) (data []byte, exists bool, err error)
type ResolveHD1MaterialFunc func(unitID stingray.FileID, materialName stingray.ThinHash) (materialID stingray.FileID, data []byte, exists bool, err error)
