package calendarconn

import "testing"

func TestMemoryRepository_Contract(t *testing.T) {
	runRepositoryContract(t, func(t *testing.T) Repository {
		return NewMemoryRepository()
	})
}
