package inventory

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ListCategories returns the operator's categories with a count of the units
// still in service in each.
func (s *Store) ListCategories(ctx context.Context) ([]Category, error) {
	out := []Category{}
	err := s.db.ReadTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`select c.id::text, c.site_id::text, c.code, c.name, c.kind,
			        c.revenue_class, c.max_occupancy, c.min_electricity_amp,
			        c.pets_allowed, c.accessible, c.sanitary, c.sort_order,
			        count(u.id) filter (where u.status = 'active')
			   from unit_category c
			   left join unit u on u.category_id = c.id
			  group by c.id, c.site_id, c.code, c.name, c.kind,
			           c.revenue_class, c.max_occupancy, c.min_electricity_amp,
			           c.pets_allowed, c.accessible, c.sanitary, c.sort_order
			  order by c.sort_order, c.code`)
		if err != nil {
			return fmt.Errorf("query categories: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var c Category
			if err := rows.Scan(
				&c.ID, &c.SiteID, &c.Code, &c.Name, &c.Kind, &c.RevenueClass,
				&c.MaxOccupancy, &c.MinElectricityAmp, &c.PetsAllowed,
				&c.Accessible, &c.Sanitary, &c.SortOrder, &c.Units,
			); err != nil {
				return fmt.Errorf("scan category: %w", err)
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ListUnits returns the operator's units, newest categories first by sort
// order. Retired units are included: staff need to see them to bring one back.
func (s *Store) ListUnits(ctx context.Context) ([]Unit, error) {
	out := []Unit{}
	err := s.db.ReadTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, unitSelect+` order by u.sort_order, u.code`)
		if err != nil {
			return fmt.Errorf("query units: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			u, err := scanUnit(rows)
			if err != nil {
				return err
			}
			out = append(out, u)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

const unitSelect = `
	select u.id::text, u.site_id::text, u.category_id::text, c.code,
	       u.code, u.status, u.electricity_amp, u.area_m2,
	       u.max_vehicle_length_m, u.max_occupancy, u.pets_allowed,
	       u.accessible, u.has_water, u.has_greywater, u.has_sewer,
	       u.surface, u.shade, u.drive_through, u.sanitary, u.view,
	       u.cleanliness, u.sort_order
	  from unit u
	  join unit_category c on c.id = u.category_id`

func scanUnit(rows pgx.Rows) (Unit, error) {
	var u Unit
	if err := rows.Scan(
		&u.ID, &u.SiteID, &u.CategoryID, &u.CategoryCode, &u.Code, &u.Status,
		&u.ElectricityAmp, &u.AreaM2, &u.MaxVehicleLengthM, &u.MaxOccupancy,
		&u.PetsAllowed, &u.Accessible, &u.HasWater, &u.HasGreywater,
		&u.HasSewer, &u.Surface, &u.Shade, &u.DriveThrough, &u.Sanitary,
		&u.View, &u.Cleanliness, &u.SortOrder,
	); err != nil {
		return Unit{}, fmt.Errorf("scan unit: %w", err)
	}
	return u, nil
}
