-- Blame snapshots are derived data (gzipped JSON, safe to delete). Some stored
-- a guess made while history was still being indexed: lines credited to the
-- oldest commit the walk read, kept for good. A blame no longer stores a
-- guess, so clear what is there, and blames come back as they are asked for.
delete from blame_snapshots
