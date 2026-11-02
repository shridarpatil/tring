-- masking_sessions was created for an in-memory number-masking pool that was
-- never wired up; nothing reads or writes it. The 'masking' number type stays.
DROP TABLE IF EXISTS masking_sessions;
