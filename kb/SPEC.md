# KB Authoring Spec — WANFANG INSULATION PACKAGING MATERIAL CO., LTD (万方建材)

> SINGLE SOURCE OF TRUTH for every knowledge-base document in this corpus.
> Every code, price, spec and term below is canonical. Never invent a conflicting
> value. If a document needs a fact not listed here, derive it consistently from
> what IS here, or route the customer to the sales team instead of guessing.
>
> **Status note:** this corpus is loaded into the TEST tenant (user 7) for load
> testing. Company-specific commercial figures are drafts pending the company's
> verification. Technical values are genuine industry-standard EPS values.

## 0. Output contract (mandatory for every document)

- Format: Markdown. Start with one `#` title line, then the header block, then `##` sections.
- Header block, exactly this shape:

```
# <ចំណងជើងឯកសារ / 文档标题>
> ឯកសារផ្ទៃក្នុង — WANFANG INSULATION PACKAGING MATERIAL CO., LTD (万方建材)
> កូដឯកសារ: SVN-0XX · ធ្វើបច្ចុប្បន្នភាព: 2026-09-15
> ប្រធានបទ: <ប្រធានបទខ្លី / 主题>
```

- **Length: at least 3,500 characters of body text**, target 4,000–7,000. Thin stubs are
  worthless — the point of this corpus is depth. Use `##` sections, bullets and tables
  to carry real operational detail (numbers, tolerances, schedules, checklists).
- Each document must be **self-contained**: repeat the product code, price or spec it
  discusses inline instead of saying "see other document".
- Keep units exactly as written here: `kg/m³`, `W/(m·K)`, `kPa`, `mm`, `m²`, `m³`, `$`, `°C`.
- Language per document is assigned in your task. Do not mix languages inside one document
  except for unavoidable technical terms (EPS, XPS, TDS, MOQ) and product codes.

## 1. Company identity (canonical — from the company's own quotation)

- English name: **WANFANG INSULATION PACKAGING MATERIAL CO., LTD**
- Chinese name: **万方建材**
- Business: manufacturer & supplier of EPS (Expanded Polystyrene) thermal-insulation
  and packaging materials in Cambodia.
- Factory / office address: **#777, Road No. 2, Kvanmeas village, Poutsor commune,
  Bati district, Takeo province, Cambodia**
- Tel: **+855 06663800**
- Email: **xuchamnan@gmail.com**
- Website / assistant domain: **cs.wanfanginsulationmaterial.com**
- Customer service channels: website chat widget, Telegram, LINE, Meta (Facebook) Page
- Languages served: **ខ្មែរ (Khmer), 中文 (Chinese), English**
- Working hours: ចន្ទ–សៅរ៍ 8:00–17:30 (lunch 12:00–13:30); Sunday & public holidays closed.
- Reference customer already on file: **ISI STEEL CO., LTD**
- Bank: **ABA Bank** (both transfer and KHQR)

## 2. Product lines (canonical codes)

| Code | Product | Khmer | Chinese | Unit basis |
|---|---|---|---|---|
| **EPS-P** | EPS Sandwich Panel | ផ្ទាំងសាំងវិច EPS | 彩钢夹芯板 / EPS夹芯板 | per KG **and** per m² |
| **EPS-S** | EPS Sheet / Board | សន្លឹក EPS | EPS板材 / 泡沫板 | per m³ |
| **EPS-B** | EPS Block | ដុំ EPS | EPS大块 / 泡沫大板 | per m³ |
| **EPS-C** | Custom cut / moulded shape | កាត់តាមទំហំ / ពិសេស | 定制切割 / 成型 | per piece (quoted) |
| **EPS-R** | EPS raw bead | គ្រាប់ EPS ឆៅ | EPS原料珠粒 | per KG |

## 3. Canonical price list (draft — company to verify)

**Anchor facts from the company's real quotation (DO NOT change):**
- **EPS PANEL = $2.80 / KG**
- **EPS SHEET (10 kg/m³) = $32.00 / m³**

Derived canonical rate card (use exactly these):

### EPS-S — EPS Sheet, price per m³ by density

| ដង់ស៊ីតេ (kg/m³) | តម្លៃ / m³ | 价格 / m³ |
|---|---|---|
| 10 | **$32.00** | $32.00 |
| 12 | $38.00 | $38.00 |
| 15 | $45.00 | $45.00 |
| 18 | $52.00 | $52.00 |
| 20 | $58.00 | $58.00 |
| 25 | $72.00 | $72.00 |
| 30 | $86.00 | $86.00 |

### Other lines

| Line | Basis | Price |
|---|---|---|
| EPS-B (block) | per m³, 10 kg/m³ | $30.00 |
| EPS-B (block) | per m³, 15 kg/m³ | $43.00 |
| EPS-C (custom cut) | surcharge on sheet price | **+15%** |
| EPS-C (moulded, new tooling) | per piece | quoted per drawing |
| EPS-R (raw bead) | per KG | $1.90 |
| EPS-P (panel) | per KG | **$2.80** |
| EPS-P (panel, 50 mm, steel-faced) | per m² | $9.80 |
| EPS-P (panel, 75 mm, steel-faced) | per m² | $12.60 |
| EPS-P (panel, 100 mm, steel-faced) | per m² | $15.40 |
| EPS-P (panel, 150 mm, steel-faced) | per m² | $20.80 |
| EPS-P (panel, 200 mm, steel-faced) | per m² | $26.50 |

## 4. Canonical technical values (genuine EPS industry data)

### Thermal conductivity λ, W/(m·K) at ~25 °C

| Density kg/m³ | 10 | 12 | 15 | 18 | 20 | 25 | 30 |
|---|---|---|---|---|---|---|---|
| λ | 0.038 | 0.036 | 0.034 | 0.033 | 0.033 | 0.032 | 0.031 |

### Compressive strength at 10% deformation (kPa)

| Density kg/m³ | 10 | 15 | 20 | 25 | 30 |
|---|---|---|---|---|---|
| σ10 | 50 | 90 | 130 | 180 | 230 |

### Other physical properties

- Flexural strength: 100–400 kPa (rises with density)
- Dimensional stability: ≤ 0.5 % (70 °C, 48 h)
- Water absorption: ≤ 2 % by volume (long-term immersion)
- Water vapour diffusion resistance factor μ: 20–40
- Service temperature range: **−40 °C to +70 °C** (short-term up to 80 °C)
- Fire behaviour: standard grade is combustible; **FR (flame-retardant) grade
  available on request** — always state that FR must be specified at order time.
- Recycling: 100 % recyclable; contains no CFC/HCFC (pentane blown)

### Standard dimensions

- EPS-S sheet: 1000×2000 mm, 1200×2400 mm, 1000×3000 mm; thickness 10–500 mm;
  thickness tolerance ±1 mm (cut) / ±2 mm (moulded)
- EPS-B block: 1000×1000×500 mm, 2000×1000×500 mm (larger on request)
- EPS-P panel: width 950 / 1000 / 1150 mm; length up to 6000 mm;
  core thickness **50 / 75 / 100 / 150 / 200 mm**
- Panel facings available: colour-coated steel (0.326 / 0.376 / 0.426 mm),
  fibre-cement board, aluminium foil, gypsum board.

## 5. Commercial terms (canonical)

- **Delivery lead time: 2 working days after order confirmation** (this is the
  company's own stated term — treat as canonical). Stock items in Phnom Penh: 1 day.
- Delivery: to the customer's designated location.
- **MOQ**: 20 m³ for EPS-S sheets (≈ one truck load); single sheets sellable in Phnom Penh.
- Volume discount on EPS-S / EPS-B: **100+ m³ → 5 %**, **300+ m³ → 8 %**, **500+ m³ → 12 %**.
  Not combinable with promotional pricing.
- **Payment**: ABA Bank transfer or KHQR. New customers: 50 % deposit, 50 % before
  delivery. **Approved accounts: monthly settlement (30 days)** — this term is already
  in force for ISI STEEL CO., LTD.
- Credit terms require a signed supply agreement; late payment suspends new deliveries.
- Quotation validity: **15 days** from issue date.
- Currency: USD. Riel accepted at the day's rate.
- Truck capacity reference: 5 T ≈ 12 m³ · 10 T ≈ 25 m³ · 20 T ≈ 50 m³ of 10 kg/m³ sheet.
- Delivery charges: free within Takeo province; Phnom Penh and other provinces quoted
  per trip by truck size — always route exact freight to the sales team.

## 6. Quality, claims and returns (canonical)

- Every delivery ships with a delivery note stating density, dimensions, quantity and
  batch number. The customer must inspect on arrival.
- **Defect / shortage claims must be raised within 3 working days of delivery** with
  photos of the goods, the delivery note and the batch number.
- Manufacturing defects (wrong density, wrong dimensions, collapsed or deformed board):
  replaced free of charge, including freight.
- Not covered: damage caused by improper storage (direct sunlight, prolonged rain,
  contact with solvents/fuel), cutting errors by the customer, or use above 70 °C.
- EPS must be stored under shade, off the ground, away from open flame and solvents.
- Returns of correctly supplied material are accepted only by prior agreement and are
  subject to a restocking charge.

## 7. Application baselines (canonical)

- **Cold storage** (បន្ទប់ត្រជាក់ / 冷库): wall & ceiling 100–200 mm depending on room
  temperature — chiller 100 mm, freezer 150 mm, blast freezer 200 mm.
- **Building roof insulation**: 25–50 mm sheet; floor / slab 20–40 mm.
- **Wall insulation**: 20–50 mm.
- **Packaging**: 10–20 kg/m³ sheet, 10–50 mm thickness for protection; custom moulded
  EPS for appliances, electronics and furniture.
- **Seafood / fruit export**: 15–20 kg/m³ boxes with 20–30 mm wall.
- **Geofoam / void fill**: 10–15 kg/m³ lightweight fill.
- Typical density selection: packaging 10–15 · building insulation 15–20 ·
  cold storage 20–30 · load-bearing floor 25–30.

## 8. Document numbering

Numbering restarts at SVN-001 for this corpus (the old SVN-001..019 water-purifier
fixture is retired). Assign exactly the numbers given in your task. Do not renumber.
