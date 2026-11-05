-- +goose Up
INSERT INTO reference.dictionaries (code, kind, identity, alias_kinds, limits)
SELECT supplied.code, supplied.kind, false, brand.alias_kinds, brand.limits
FROM (VALUES
    ('battery_chemistry', 'closed'),
    ('body_material', 'closed'),
    ('book_binding', 'closed'),
    ('color', 'closed'),
    ('cookware_material', 'closed'),
    ('country', 'open'),
    ('firmness', 'closed'),
    ('gender', 'closed'),
    ('jewelry_metal', 'closed'),
    ('material', 'open'),
    ('screen_matrix', 'closed'),
    ('season', 'closed'),
    ('size_clothing', 'closed'),
    ('size_kids_height', 'closed'),
    ('size_ring', 'closed'),
    ('size_shoes_eu', 'closed'),
    ('storage_type', 'closed'),
    ('textile_material', 'closed')
) AS supplied(code, kind)
CROSS JOIN reference.dictionaries AS brand
WHERE brand.code = 'brand'
ON CONFLICT DO NOTHING;

-- +goose Down
-- Словари и накопленные записи сохраняются при откате миграции.
SELECT 1;
