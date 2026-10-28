-- +goose Up
-- Условия тарифа защищены после первого платежа или интервала подписки.
-- FOR SHARE в check_payment/check_subscription_type сериализует проверку с UPDATE.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION colab.protect_subscription_type() RETURNS trigger
LANGUAGE plpgsql SET search_path = colab, pg_temp AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF OLD.price_rub = 0 THEN
            RAISE EXCEPTION 'default subscription type cannot be deleted' USING ERRCODE = '23514';
        END IF;
        RETURN OLD;
    END IF;
    IF (NEW.runtime_limit, NEW.ram_limit, NEW.cpu_limit, NEW.price_rub, NEW.duration)
       IS DISTINCT FROM
       (OLD.runtime_limit, OLD.ram_limit, OLD.cpu_limit, OLD.price_rub, OLD.duration)
       AND (OLD.price_rub = 0 OR EXISTS (
           SELECT 1
             FROM user_subscription
            WHERE subscription_type_id = OLD.id
       ) OR EXISTS (
           SELECT 1 FROM payment WHERE subscription_type_id = OLD.id
       )) THEN
        RAISE EXCEPTION 'used or default subscription terms are immutable' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- Версия строки возрастает даже при переводе часов назад.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION colab.touch_row() RETURNS trigger
LANGUAGE plpgsql SET search_path = colab, pg_temp AS $$
BEGIN
    IF NEW.id <> OLD.id OR NEW.created_at <> OLD.created_at THEN
        RAISE EXCEPTION 'id and created_at are immutable' USING ERRCODE = '23514';
    END IF;
    IF TG_TABLE_NAME = 'project' THEN
        IF NEW.user_id <> OLD.user_id THEN
            RAISE EXCEPTION 'project owner is immutable; copy the project' USING ERRCODE = '23514';
        END IF;
    END IF;
    NEW.updated_at := greatest(clock_timestamp(), OLD.updated_at + INTERVAL '1 microsecond');
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION colab.touch_row() RETURNS trigger
LANGUAGE plpgsql SET search_path = colab, pg_temp AS $$
BEGIN
    IF NEW.id <> OLD.id OR NEW.created_at <> OLD.created_at THEN
        RAISE EXCEPTION 'id and created_at are immutable' USING ERRCODE = '23514';
    END IF;
    IF TG_TABLE_NAME = 'project' THEN
        IF NEW.user_id <> OLD.user_id THEN
            RAISE EXCEPTION 'project owner is immutable; copy the project' USING ERRCODE = '23514';
        END IF;
    END IF;
    NEW.updated_at := clock_timestamp();
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION colab.protect_subscription_type() RETURNS trigger
LANGUAGE plpgsql SET search_path = colab, pg_temp AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF OLD.price_rub = 0 THEN
            RAISE EXCEPTION 'default subscription type cannot be deleted' USING ERRCODE = '23514';
        END IF;
        RETURN OLD;
    END IF;
    IF (NEW.runtime_limit, NEW.ram_limit, NEW.cpu_limit, NEW.price_rub, NEW.duration)
       IS DISTINCT FROM
       (OLD.runtime_limit, OLD.ram_limit, OLD.cpu_limit, OLD.price_rub, OLD.duration)
       AND (OLD.price_rub = 0 OR EXISTS (
           SELECT 1
             FROM user_subscription
            WHERE subscription_type_id = OLD.id
       )) THEN
        RAISE EXCEPTION 'used or default subscription terms are immutable' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
