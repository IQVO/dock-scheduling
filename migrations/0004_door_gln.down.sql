-- 0004_door_gln.down.sql: recreate the previous schema (0003) exactly. The
-- column check goes with the column; the GLNs are re-learned from the next
-- LocationSlotRegistered of each door.
ALTER TABLE doors DROP COLUMN IF EXISTS gln;
