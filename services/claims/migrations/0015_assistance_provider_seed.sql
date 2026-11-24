-- HU-W2.3-03: the providers of the on-site assistance, loaded as reference
-- data (assumption S14): tow trucks and roadside assistance around the
-- requests of the mockup (Autopista Norte in Bogotá and the road Cali -
-- Palmira), one beyond the initial radius and one in the south. They bring no
-- plumbing: a plumbing emergency at home goes to the plumbers of
-- 0009_provider_seed. They are pending replacement by the business:
-- operations keeps them with the API of the registry, and ON CONFLICT leaves
-- what it edited as it is. The memory store loads the same providers
-- (memory.ReferenceProviders).
INSERT INTO provider (id, name, specialties, latitude, longitude, address, contact_email, contact_phone,
                      availability, updated_by, updated_at)
VALUES
    ('prv-gruas-autopista-norte', 'Grúas Autopista Norte', ARRAY['towing'],
     4.7793, -74.0452, 'Autopista Norte # 174-30, Bogotá', 'despachos@gruasautopistanorte.example.com',
     '+576015550301', 'available', 'reference-data', '2026-11-15T00:00:00Z'),
    ('prv-auxilio-vial-norte', 'Auxilio Vial Norte', ARRAY['roadside_assistance', 'towing'],
     4.7514, -74.0452, 'Autopista Norte # 145-20, Bogotá', 'operaciones@auxiliovialnorte.example.com',
     '+576015550302', 'available', 'reference-data', '2026-11-15T00:00:00Z'),
    ('prv-gruas-calle-80', 'Grúas Calle 80', ARRAY['towing'],
     4.6822, -74.0452, 'Calle 80 # 11-20, Bogotá', 'servicio@gruascalle80.example.com',
     NULL, 'available', 'reference-data', '2026-11-15T00:00:00Z'),
    ('prv-asistencia-vial-sur', 'Asistencia Vial Sur', ARRAY['roadside_assistance'],
     4.58, -74.14, 'Autopista Sur # 60-51, Bogotá', 'turnos@asistenciavialsur.example.com',
     '+576015550304', 'available', 'reference-data', '2026-11-15T00:00:00Z'),
    ('prv-gruas-del-valle', 'Grúas del Valle', ARRAY['towing', 'roadside_assistance'],
     3.506, -76.41, 'Vía Cali–Palmira km 6, Palmira', 'despachos@gruasdelvalle.example.com',
     '+576025550305', 'available', 'reference-data', '2026-11-15T00:00:00Z')
ON CONFLICT (id) DO NOTHING;
