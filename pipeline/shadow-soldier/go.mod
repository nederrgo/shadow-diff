module github.com/shadow-diff/shadow-soldier

go 1.26.0

require (
	github.com/jackc/pgx/v5 v5.10.0
	github.com/shadow-diff/beruclient v0.0.0
	go.mongodb.org/mongo-driver v1.17.4
)

replace github.com/shadow-diff/beruclient => ../pkg/beruclient
