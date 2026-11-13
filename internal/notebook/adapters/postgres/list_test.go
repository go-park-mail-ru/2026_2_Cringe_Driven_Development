package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/notebook/usecase"
	"github.com/pashagolub/pgxmock/v5"
)

func TestNotebookListTransaction(t *testing.T) {
	failure := errors.New("database unavailable")
	for _, stage := range []string{"begin", "isolation", "counts", "query", "scan", "rows", "commit", "success"} {
		t.Run(stage, func(t *testing.T) {
			db, err := pgxmock.NewPool()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			begin := db.ExpectBegin()
			if stage == "begin" {
				begin.WillReturnError(failure)
			} else {
				isolation := db.ExpectExec("SET TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY")
				if stage == "isolation" {
					isolation.WillReturnError(failure)
				} else {
					isolation.WillReturnResult(pgxmock.NewResult("SET", 0))
					counts := db.ExpectQuery(`SELECT count\(\*\), count\(\*\) FILTER`).WithArgs(int64(7))
					if stage == "counts" {
						counts.WillReturnError(failure)
					} else {
						counts.WillReturnRows(pgxmock.NewRows([]string{"total", "matched"}).AddRow(int64(9), int64(9)))
						page := db.ExpectQuery(`SELECT id,name,cells_count,preview_code,updated_at FROM notebooks`).WithArgs(int64(7), 25)
						switch stage {
						case "query":
							page.WillReturnError(failure)
						case "scan":
							page.WillReturnRows(pgxmock.NewRows([]string{"id", "name", "cells", "preview", "updated"}).AddRow("not-an-int", "name", 1, "", time.Now()))
						case "rows":
							page.WillReturnRows(pgxmock.NewRows([]string{"id", "name", "cells", "preview", "updated"}).AddRow(int64(3), "name", 1, "", time.Now()).RowError(0, failure))
						default:
							page.WillReturnRows(pgxmock.NewRows([]string{"id", "name", "cells", "preview", "updated"}))
						}
						switch stage {
						case "success":
							db.ExpectCommit()
						case "commit":
							db.ExpectCommit().WillReturnError(failure)
						}
					}
				}
				db.ExpectRollback()
			}
			got, err := NewNotebookRepository(db).ListPage(context.Background(), 7, usecase.ListQuery{Limit: 24})
			if stage == "success" {
				if err != nil || got.Items == nil || got.TotalCount != 9 || got.MatchedCount != 9 {
					t.Fatalf("%+v %v", got, err)
				}
			} else if err == nil {
				t.Fatal("database failure hidden")
			}
			if err := db.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
