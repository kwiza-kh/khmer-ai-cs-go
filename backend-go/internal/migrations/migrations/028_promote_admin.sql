-- ============================================
-- 028 — promote the bootstrap admin to platform super-admin.
-- Separate transaction from 027 so the new enum value is usable.
-- ============================================

UPDATE users SET role = 'platform_admin' WHERE username = 'admin' AND role = 'admin';
