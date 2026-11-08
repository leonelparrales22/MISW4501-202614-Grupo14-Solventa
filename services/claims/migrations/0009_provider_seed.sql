-- HU-W2.3-01: the minimal registry of providers, loaded as reference data
-- (assumption S13). Providers of Bogotá that cover every specialty, the three
-- of the mockup among them. They are pending replacement by the business:
-- operations keeps them with the API of the registry, and ON CONFLICT leaves
-- what it edited as it is. The memory store loads the same providers
-- (memory.ReferenceProviders).
INSERT INTO provider (id, name, specialties, latitude, longitude, address, contact_email, contact_phone,
                      availability, updated_by, updated_at)
VALUES
    ('prv-servicios-hogar-norte', 'Servicios Hogar Norte', ARRAY['plumbing', 'damp_proofing'],
     4.694, -74.0364, 'Calle 116 # 9-72, Bogotá', 'despachos@hogarnorte.example.com', '+576015550102',
     'available', 'reference-data', '2026-11-07T00:00:00Z'),
    ('prv-reparaciones-chapinero', 'Reparaciones Chapinero', ARRAY['plumbing'],
     4.645, -74.06, 'Calle 63 # 13-20, Bogotá', 'servicio@reparacioneschapinero.example.com', '+576015550103',
     'available', 'reference-data', '2026-11-07T00:00:00Z'),
    ('prv-tecnicos-unidos', 'Técnicos Unidos', ARRAY['plumbing', 'damp_proofing', 'painting'],
     4.7246, -74.0758, 'Avenida Suba # 115-58, Bogotá', 'operaciones@tecnicosunidos.example.com', NULL,
     'available', 'reference-data', '2026-11-07T00:00:00Z'),
    ('prv-peritos-autos-andinos', 'Peritos Autos Andinos', ARRAY['vehicle_assessment', 'bodywork'],
     4.665, -74.07, 'Carrera 30 # 75-40, Bogotá', 'peritajes@autosandinos.example.com', '+576015550104',
     'available', 'reference-data', '2026-11-07T00:00:00Z'),
    ('prv-peritaje-express-sur', 'Peritaje Express Sur', ARRAY['vehicle_assessment'],
     4.61, -74.145, 'Avenida Boyacá # 38-20 Sur, Bogotá', 'citas@peritajeexpress.example.com', '+576015550105',
     'available', 'reference-data', '2026-11-07T00:00:00Z'),
    ('prv-vidrios-capital', 'Vidrios Capital', ARRAY['auto_glass'],
     4.639, -74.083, 'Calle 45 # 20-15, Bogotá', 'servicio@vidrioscapital.example.com', '+576015550106',
     'available', 'reference-data', '2026-11-07T00:00:00Z'),
    ('prv-latoneria-pintura-la-80', 'Latonería y Pintura La 80', ARRAY['bodywork', 'painting'],
     4.69, -74.087, 'Calle 80 # 68-30, Bogotá', 'taller@latonerialaochenta.example.com', '+576015550107',
     'available', 'reference-data', '2026-11-07T00:00:00Z'),
    ('prv-humedades-y-acabados', 'Humedades y Acabados', ARRAY['damp_proofing', 'painting'],
     4.71, -74.03, 'Calle 140 # 11-45, Bogotá', 'contacto@humedadesyacabados.example.com', '+576015550108',
     'available', 'reference-data', '2026-11-07T00:00:00Z')
ON CONFLICT (id) DO NOTHING;
