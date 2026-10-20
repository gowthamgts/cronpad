-- +goose Up
CREATE TABLE hello_messages (id INTEGER PRIMARY KEY, message TEXT NOT NULL);
-- +goose Down
DROP TABLE hello_messages;
