INSERT OR REPLACE INTO config(key,value) VALUES('min_mem_available_mb','1'),('terminal_backend','herdr');
DROP INDEX idx_util_samples_ts;
DROP TABLE util_samples;
