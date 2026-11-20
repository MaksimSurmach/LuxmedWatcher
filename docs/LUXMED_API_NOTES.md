# LuxMed portal availability

On 20 November 2026, an authenticated browser session loaded the reservation form, selected a service, searched, displayed results and applied a time filter. The search returned HTTP 200 and visible slots. The capture began after login. The source HAR contains private session and patient data and stays outside the repository.

All paths below are relative to `/PatientPortal/NewPortal`.

| Portal call | What the capture showed |
| --- | --- |
| `GET Dictionary/cities` and `Dictionary/serviceVariantsGroups` | City and service catalogs |
| `GET Dictionary/facilitiesAndDoctors` | Facility and doctor catalogs for a city and service |
| `GET terms/GetMaxSearchDays` | Search horizon for the selected city and service |
| `GET terms/index` | Search dates and location, service and process context. Response includes `termsForService.termsForDays` and per-day counts under `termsInfoForDays` |
| `GET terms/oneDayTerms` | Additional slots for one date. The portal reuses the search process ID, sets the same date in both date parameters, and supplies `expectedTermsNumber` |
| `POST PNM/Occurrence` | The portal recorded the user's selection in a service-specific questionnaire before returning results for the captured example |

The client uses 14-day preview requests and at most four additional day requests per check. It works with verified slots from partial responses and surfaces an incomplete result as such. Each search creates its own correlation ID. The client does not import browser cookies, questionnaire executions or referrals from the capture.

The authenticated browser run verifies the first-party portal interaction. Automated tests verify the Go client against the observed response shapes. Fresh login, MFA, and direct Go calls using a real account remain separate live verification steps. A search that requires portal interaction can be finished in the patient portal.
