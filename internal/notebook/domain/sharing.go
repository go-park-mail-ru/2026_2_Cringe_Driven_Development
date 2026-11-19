package domain

// SharingMode определяет, кому доступно чтение блокнота.
type SharingMode string

const (
	SharingModePrivate    SharingMode = "private"
	SharingModeRestricted SharingMode = "restricted"
	SharingModePublic     SharingMode = "public"
)

type NotebookReader struct {
	ID    int64
	Login string
}

// SharingSettings содержит настройки доступа к блокноту.
type SharingSettings struct {
	Mode    SharingMode
	Readers []NotebookReader
}

// NotebookAccess содержит только возможности текущего посетителя.
type NotebookAccess struct {
	IsOwner          bool
	CanEdit          bool
	CanManageSharing bool
	CanClone         bool
}

// AccessFor вычисляет возможности после успешной проверки права чтения.
func AccessFor(ownerID, userID int64) NotebookAccess {
	owner := userID > 0 && userID == ownerID
	return NotebookAccess{IsOwner: owner, CanEdit: owner, CanManageSharing: owner, CanClone: userID > 0}
}
